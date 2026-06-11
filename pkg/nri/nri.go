package nri

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cni"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// Plugin represents a NRI plugin catching RunPodSandbox and StopPodSandbox events to
// call CNI ADD/DEL based on ResourceClaim attached to pods.
type Plugin struct {
	stub       stub.Stub
	podManager *podmanager.PodManager
	cniRuntime cni.Interface

	k8sClient                   flags.ClientSets
	networkDeviceDataUpdateChan chan types.NetworkDataChanStructList
	interfacePrefix             string
	enableDeviceMetadata        bool
	metadataUpdater             types.MetadataUpdater
}

// NewNRIPlugin creates a new NRI plugin.
func NewNRIPlugin(config *types.Config, podManager *podmanager.PodManager, cniRuntime cni.Interface, metadataUpdater types.MetadataUpdater) (*Plugin, error) {
	p := &Plugin{
		podManager:                  podManager,
		cniRuntime:                  cniRuntime,
		k8sClient:                   config.K8sClient,
		interfacePrefix:             config.Flags.DefaultInterfacePrefix,
		enableDeviceMetadata:        config.Flags.EnableDeviceMetadata,
		metadataUpdater:             metadataUpdater,
		networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 100),
	}
	var err error
	// register the NRI plugin
	nriOpts := []stub.Option{
		// https://github.com/containerd/nri/pull/173
		// Otherwise it silently exits the program
		stub.WithOnClose(func() {
			klog.Infof("%s NRI plugin closed canceling context", consts.DriverName)
			config.CancelMainCtx(fmt.Errorf("NRI plugin closed"))
		}),
	}

	p.stub, err = stub.New(p, nriOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin stub: %w", err)
	}

	return p, nil
}

// Start starts the NRI plugin.
func (p *Plugin) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("NRI Start")
	logger.Info("Starting NRI plugin")
	err := p.stub.Start(ctx)
	if err != nil {
		logger.Error(err, "Failed to start NRI plugin")
		return fmt.Errorf("failed to start NRI plugin: %w", err)
	}

	go p.updateNetworkDeviceDataRunner(ctx)
	return nil
}

// Stop stops the NRI plugin.
func (p *Plugin) Stop() {
	p.stub.Stop()
	close(p.networkDeviceDataUpdateChan)
}

// RunPodSandbox runs the CNI ADD operation for each device in the devices list.
func (p *Plugin) RunPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	logger := klog.FromContext(ctx).WithName("NRI RunPodSandbox")
	logger.Info("RunPodSandbox", "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)

	devices, found := p.podManager.GetDevicesByPodUID(k8stypes.UID(pod.Uid))
	if !found {
		logger.Info("No prepared devices found for pod", "pod.UID", pod.Uid)
		return nil
	}

	// if we don't have a network namespace, we can't attach networks
	// so we skip the network attachment
	networkNamespace := getNetworkNamespace(pod)
	if networkNamespace == "" {
		logger.Info("No network namespace found for pod skipping network attachment", "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)
		return nil
	}

	networkDevicesData := types.NetworkDataChanStructList{}
	for _, device := range devices {
		networkDeviceData, cniResultMap, err := p.cniRuntime.AttachNetwork(ctx, pod, networkNamespace, device)
		if err != nil {
			logger.Error(err, "Failed to attach network", "deviceName", device.Device.DeviceName, "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)
			return fmt.Errorf("failed to attach network: %w", err)
		}
		// Parse NetAttachDefConfig into map[string]interface{} for CNIConfig
		cniConfigMap := map[string]interface{}{}
		if device.NetAttachDefConfig != "" {
			if err := json.Unmarshal([]byte(device.NetAttachDefConfig), &cniConfigMap); err != nil {
				logger.V(2).Info("Failed to unmarshal NetAttachDefConfig, proceeding with empty CNIConfig", "error", err.Error())
				cniConfigMap = map[string]interface{}{}
			}
		}

		networkDevicesData = append(networkDevicesData, &types.NetworkDataChanStruct{
			PreparedDevice:    device,
			NetworkDeviceData: networkDeviceData,
			CNIConfig:         cniConfigMap,
			CNIResult:         cniResultMap,
		})
		logger.Info("Attached network", "deviceName", device.Device.DeviceName, "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace, "networkDeviceData", networkDeviceData)
	}

	// Refresh request metadata synchronously so runtime CNI fields are available
	// when the pod starts.
	if err := p.updateRequestMetadataBeforeSandboxStart(ctx, networkDevicesData); err != nil {
		logger.Error(err, "Failed to update request metadata before pod start", "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)
		return fmt.Errorf("failed to update request metadata before pod start: %w", err)
	}

	// Claim status and checkpoint updates are still done asynchronously to keep
	// the NRI hook within its timeout budget.
	p.networkDeviceDataUpdateChan <- networkDevicesData
	return nil
}

// StopPodSandbox runs the CNI DEL operation for each device in the devices list.
func (p *Plugin) StopPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	logger := klog.FromContext(ctx).WithName("NRI StopPodSandbox")
	logger.Info("StopPodSandbox", "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)

	devices, found := p.podManager.GetDevicesByPodUID(k8stypes.UID(pod.Uid))
	if !found {
		logger.Info("No prepared devices found for pod", "pod.UID", pod.Uid)
		return nil
	}

	networkNamespace := getNetworkNamespace(pod)
	if networkNamespace == "" {
		return fmt.Errorf("error getting network namespace for pod '%s' in namespace '%s'", pod.Name, pod.Namespace)
	}

	for _, device := range devices {
		logger.Info("Detaching network", "device", device)
		err := p.cniRuntime.DetachNetwork(ctx, pod, networkNamespace, device)
		if err != nil {
			logger.Error(err, "Failed to detach network", "deviceName", device.Device.DeviceName, "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)
			return fmt.Errorf("error CNI.DetachNetwork for pod '%s' (uid: %s) in namespace '%s': %v", pod.Name, pod.Uid, pod.Namespace, err)
		}
	}
	return nil
}

// updateNetworkDeviceDataRunner is a goroutine that updates the network device data
// for each pod in the networkDeviceDataUpdateChan.
// we use it so we don't block the CNI ADD/DEL operations as we are limited by the NRI plugin timeout
func (p *Plugin) updateNetworkDeviceDataRunner(ctx context.Context) {
	for {
		select {
		case networkDeviceDataList := <-p.networkDeviceDataUpdateChan:
			p.updateNetworkDeviceData(ctx, networkDeviceDataList)
		case <-ctx.Done():
			return
		}
	}
}

// updateNetworkDeviceData updates claim status and persisted prepared-device state
// for each pod in the networkDataChanStructList. This runs asynchronously so CNI
// ADD/DEL operations are not blocked by API retries.
func (p *Plugin) updateNetworkDeviceData(ctx context.Context, networkDataChanStructList types.NetworkDataChanStructList) {
	logger := klog.FromContext(ctx).WithName("updateNetworkDeviceData")
	logger.Info("Updating network device data", "networkDataChanStructList", networkDataChanStructList)

	groupedByClaim := p.groupNetworkDataByClaim(networkDataChanStructList)
	logger.V(2).Info("Grouped network updates by claim", "claimCount", len(groupedByClaim))
	for claimKey, claimUpdates := range groupedByClaim {
		claim := &resourceapi.ResourceClaim{}
		err := p.k8sClient.Client.Get(ctx, client.ObjectKey{
			Name:      claimKey.name,
			Namespace: claimKey.namespace,
		}, claim)
		if err != nil {
			logger.Error(err, "Failed to get claim object", "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			continue
		}

		statusDeviceIndex := p.buildClaimStatusDeviceIndex(claim)
		hasClaimStatusUpdates := false
		for _, networkDataChanStruct := range claimUpdates {
			if networkDataChanStruct == nil || networkDataChanStruct.PreparedDevice == nil {
				logger.V(2).Info("Skipping invalid network update entry", "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
				continue
			}
			if err := p.podManager.UpdatePreparedDeviceNetworkData(
				networkDataChanStruct.PreparedDevice,
				networkDataChanStruct.NetworkDeviceData,
			); err != nil {
				logger.Error(err, "Failed to persist device network data before claim update", "claim", claim.UID, "deviceName", networkDataChanStruct.PreparedDevice.Device.DeviceName)
				continue
			}

			if p.updateClaimDeviceStatus(claim, statusDeviceIndex, networkDataChanStruct) {
				hasClaimStatusUpdates = true
			}
		}

		if !hasClaimStatusUpdates {
			logger.V(2).Info("No claim status updates generated for claim", "claim", claim.UID, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			continue
		}
		if err := p.updateClaimNetworkDataWithRetry(ctx, claim); err != nil {
			logger.Error(err, "Failed to update claim network data", "claim", claim.UID)
			continue
		}
		logger.V(2).Info("Successfully updated claim network data", "claim", claim.UID, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
	}
}

type networkClaimKey struct {
	namespace string
	name      string
}

type claimStatusDeviceKey struct {
	driver string
	pool   string
	device string
}

func (p *Plugin) groupNetworkDataByClaim(
	networkDataChanStructList types.NetworkDataChanStructList,
) map[networkClaimKey]types.NetworkDataChanStructList {
	grouped := make(map[networkClaimKey]types.NetworkDataChanStructList)
	for _, item := range networkDataChanStructList {
		if item == nil || item.PreparedDevice == nil {
			continue
		}
		claim := item.PreparedDevice.ClaimNamespacedName
		key := networkClaimKey{
			namespace: claim.Namespace,
			name:      claim.Name,
		}
		grouped[key] = append(grouped[key], item)
	}
	return grouped
}

func (p *Plugin) buildClaimStatusDeviceIndex(claim *resourceapi.ResourceClaim) map[claimStatusDeviceKey][]int {
	index := make(map[claimStatusDeviceKey][]int, len(claim.Status.Devices))
	for idx, device := range claim.Status.Devices {
		key := claimStatusDeviceKey{
			driver: device.Driver,
			pool:   device.Pool,
			device: device.Device,
		}
		index[key] = append(index[key], idx)
	}
	return index
}

func (p *Plugin) updateClaimDeviceStatus(
	claim *resourceapi.ResourceClaim,
	statusDeviceIndex map[claimStatusDeviceKey][]int,
	networkDataChanStruct *types.NetworkDataChanStruct,
) bool {
	key := claimStatusDeviceKey{
		driver: consts.DriverName,
		pool:   networkDataChanStruct.PreparedDevice.Device.PoolName,
		device: networkDataChanStruct.PreparedDevice.Device.DeviceName,
	}
	deviceIndexes, found := statusDeviceIndex[key]
	if !found {
		return false
	}

	// Build combined Data: { vfConfig, cniConfig, cniResult } once per update.
	combined := map[string]interface{}{
		"vfConfig":  networkDataChanStruct.PreparedDevice.Config,
		"cniConfig": networkDataChanStruct.CNIConfig,
		"cniResult": networkDataChanStruct.CNIResult,
	}
	raw, rawErr := json.Marshal(combined)

	for _, idx := range deviceIndexes {
		claim.Status.Devices[idx].NetworkData = networkDataChanStruct.NetworkDeviceData
		if rawErr == nil {
			claim.Status.Devices[idx].Data = &runtime.RawExtension{Raw: raw}
		}
	}
	return true
}

// updateRequestMetadataBeforeSandboxStart refreshes kubelet plugin request metadata
// for all prepared devices in the pod before RunPodSandbox returns. It is a
// best-effort no-op when metadata is disabled or no updater is configured, and
// de-duplicates per (claim, request) updates within the same sandbox start.
func (p *Plugin) updateRequestMetadataBeforeSandboxStart(
	ctx context.Context,
	networkDataList types.NetworkDataChanStructList,
) error {
	logger := klog.FromContext(ctx).WithName("updateRequestMetadataBeforeSandboxStart")
	if !p.enableDeviceMetadata || p.metadataUpdater == nil {
		logger.V(2).Info("Skipping request metadata update before sandbox start", "metadataEnabled", p.enableDeviceMetadata, "hasMetadataUpdater", p.metadataUpdater != nil)
		return nil
	}

	updates := p.buildRequestMetadataUpdates(ctx, networkDataList)
	logger.Info("Updating request metadata before sandbox start", "requestUpdateCount", len(updates))
	if len(updates) == 0 {
		logger.V(2).Info("No request metadata updates generated before sandbox start")
		return nil
	}
	for key, update := range updates {
		logger.V(2).Info("Applying request metadata update", "claimNamespace", key.claimNamespace, "claimName", key.claimName, "requestName", key.requestName, "deviceCount", len(update.devices))
		if err := p.updateDeviceMetadata(ctx, key, update); err != nil {
			return err
		}
	}

	return nil
}

type requestMetadataKey struct {
	claimNamespace string
	claimName      string
	requestName    string
}

type requestMetadataUpdate struct {
	claimUID k8stypes.UID
	devices  []kubeletplugin.Device
}

// updateDeviceMetadata updates kubelet plugin metadata for all request names
// grouped under one (claim, request) key. It skips work when metadata is
// disabled, no updater is configured, or no devices are associated with the key.
func (p *Plugin) updateDeviceMetadata(
	ctx context.Context,
	key requestMetadataKey,
	update requestMetadataUpdate,
) error {
	logger := klog.FromContext(ctx).WithName("updateDeviceMetadata")
	if !p.enableDeviceMetadata || p.metadataUpdater == nil {
		logger.V(2).Info("Skipping request metadata update", "metadataEnabled", p.enableDeviceMetadata, "hasMetadataUpdater", p.metadataUpdater != nil, "claimNamespace", key.claimNamespace, "claimName", key.claimName, "requestName", key.requestName)
		return nil
	}
	if len(update.devices) == 0 {
		logger.V(2).Info("Skipping request metadata update with no devices", "claimNamespace", key.claimNamespace, "claimName", key.claimName, "requestName", key.requestName)
		return nil
	}
	if err := p.metadataUpdater.UpdateRequestMetadata(
		ctx,
		key.claimNamespace,
		key.claimName,
		update.claimUID,
		key.requestName,
		update.devices,
	); err != nil {
		logger.Error(err, "Failed to update request metadata", "claimNamespace", key.claimNamespace, "claimName", key.claimName, "requestName", key.requestName, "deviceCount", len(update.devices))
		return fmt.Errorf("update request metadata for %s failed: %w", key.requestName, err)
	}
	logger.V(2).Info("Updated request metadata", "claimNamespace", key.claimNamespace, "claimName", key.claimName, "requestName", key.requestName, "deviceCount", len(update.devices))
	return nil
}

func (p *Plugin) buildRequestMetadataUpdates(
	ctx context.Context,
	networkDataList types.NetworkDataChanStructList,
) map[requestMetadataKey]requestMetadataUpdate {
	logger := klog.FromContext(ctx).WithName("buildRequestMetadataUpdates")
	updates := make(map[requestMetadataKey]requestMetadataUpdate)
	for _, item := range networkDataList {
		if item == nil {
			logger.V(2).Info("Skipping nil network data item while building metadata updates")
			continue
		}
		if item.PreparedDevice == nil {
			logger.V(2).Info("Skipping metadata update item with nil prepared device")
			continue
		}
		if len(item.PreparedDevice.Device.GetRequestNames()) == 0 {
			logger.V(2).Info("Skipping metadata update item with no request names", "claimNamespace", item.PreparedDevice.ClaimNamespacedName.Namespace, "claimName", item.PreparedDevice.ClaimNamespacedName.Name, "deviceName", item.PreparedDevice.Device.DeviceName)
			continue
		}

		device := item.PreparedDevice.ToKubeletPluginDevice(item.NetworkDeviceData)
		claim := item.PreparedDevice.ClaimNamespacedName
		for _, requestName := range item.PreparedDevice.Device.GetRequestNames() {
			key := requestMetadataKey{
				claimNamespace: claim.Namespace,
				claimName:      claim.Name,
				requestName:    requestName,
			}
			update := updates[key]
			if update.devices == nil {
				update.devices = make([]kubeletplugin.Device, 0, 1)
			}
			if claim.UID != "" || update.claimUID == "" {
				update.claimUID = claim.UID
			}
			update.devices = append(update.devices, device)
			updates[key] = update
		}
	}
	logger.V(2).Info("Built request metadata updates", "requestUpdateCount", len(updates))
	return updates
}

// updateClaimNetworkDataWithRetry updates the network device data for a claim with retries.
func (p *Plugin) updateClaimNetworkDataWithRetry(ctx context.Context, claim *resourceapi.ResourceClaim) error {
	logger := klog.FromContext(ctx).WithName("updateClaimNetworkDataWithRetry")
	originalDevices := claim.Status.Devices
	err := wait.ExponentialBackoffWithContext(ctx, consts.Backoff, func(ctx context.Context) (bool, error) {
		_, updateErr := p.k8sClient.ResourceV1().ResourceClaims(claim.Namespace).UpdateStatus(ctx, claim, metav1.UpdateOptions{})
		if updateErr != nil {
			// If this is a conflict error, fetch fresh claim and copy over devices list
			if apierrors.IsConflict(updateErr) {
				logger.V(2).Info("Conflict detected, refreshing claim", "claim", claim.UID)

				freshClaim, fetchErr := p.k8sClient.ResourceV1().ResourceClaims(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
				if fetchErr != nil {
					logger.V(2).Info("Failed to fetch fresh claim", "claim", claim.UID, "error", fetchErr.Error())
					return false, nil // Continue retrying
				}

				// Copy original devices list to fresh claim
				freshClaim.Status.Devices = originalDevices
				claim = freshClaim // Use fresh claim for next retry

				logger.V(2).Info("Refreshed claim, retrying status update", "claim", claim.UID)
			} else {
				logger.V(2).Info("Retrying claim status update", "claim", claim.UID, "error", updateErr.Error())
			}
			return false, nil // Return false to continue retrying, nil to not fail immediately
		}
		return true, nil // Success
	})

	if err != nil {
		logger.Error(err, "Failed to update claim status after retries", "claim", claim.UID)
		return err
	}
	return nil
}
