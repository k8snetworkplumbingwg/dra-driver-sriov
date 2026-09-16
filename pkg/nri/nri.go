package nri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	resourceapi "k8s.io/api/resource/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

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

	k8sClient            flags.ClientSets
	interfacePrefix      string
	enableDeviceMetadata bool
	metadataUpdater      types.MetadataUpdater
	// statusBackoff paces one round of claim status retries. It is a field so
	// unit tests can shorten it; consts.Backoff when unset.
	statusBackoff wait.Backoff

	// networkDeviceDataUpdateChan hands each started sandbox's network data to
	// the runner. It is never closed: a hook may still be sending when the
	// plugin stops, so the runner is stopped through its context instead.
	networkDeviceDataUpdateChan chan types.NetworkDataChanStructList
	runnerMu                    sync.Mutex
	runnerCancel                context.CancelFunc
	runnerDone                  chan struct{}
}

var (
	errUpdateQueueFull = errors.New("network data update queue is full")
	errClaimReleased   = errors.New("claim is no longer reserved for the pod")
)

// maxRequeues bounds how often a failed claim status update goes back on the
// queue before it is dropped.
const maxRequeues = 3

// NewNRIPlugin creates a new NRI plugin.
func NewNRIPlugin(config *types.Config, podManager *podmanager.PodManager, cniRuntime cni.Interface, metadataUpdater types.MetadataUpdater) (*Plugin, error) {
	p := &Plugin{
		podManager:                  podManager,
		cniRuntime:                  cniRuntime,
		k8sClient:                   config.K8sClient,
		interfacePrefix:             config.Flags.DefaultInterfacePrefix,
		enableDeviceMetadata:        config.Flags.EnableDeviceMetadata,
		metadataUpdater:             metadataUpdater,
		statusBackoff:               consts.Backoff,
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

	p.startRunner(ctx)
	return nil
}

// Stop stops the NRI plugin: the stub first, so no new hooks arrive, then the
// runner. Updates still queued, or enqueued by a hook in flight, are dropped.
func (p *Plugin) Stop() {
	p.stub.Stop()
	p.stopRunner()
}

// startRunner runs updateNetworkDeviceDataRunner until ctx ends or stopRunner
// is called.
func (p *Plugin) startRunner(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p.runnerMu.Lock()
	p.runnerCancel, p.runnerDone = cancel, done
	p.runnerMu.Unlock()

	go func() {
		defer close(done)
		p.updateNetworkDeviceDataRunner(ctx)
	}()
}

// stopRunner cancels the runner, which abandons the update in progress and
// the backlog, and waits for it to return. Before startRunner, and after the
// first call, it does nothing.
func (p *Plugin) stopRunner() {
	p.runnerMu.Lock()
	cancel, done := p.runnerCancel, p.runnerDone
	p.runnerCancel, p.runnerDone = nil, nil
	p.runnerMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	klog.V(2).Info("NRI network data runner stopped")
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

	// Claim status and checkpoint updates are done asynchronously to keep the
	// NRI hook within its timeout budget. A full queue means the runner has
	// fallen behind, typically on API retries, and waiting here would stall
	// sandbox creation on that instead, so the update is dropped and logged.
	select {
	case p.networkDeviceDataUpdateChan <- networkDevicesData:
	default:
		logger.Error(errUpdateQueueFull, "Dropping claim status and checkpoint update", "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace, "queueSize", cap(p.networkDeviceDataUpdateChan))
	}
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
		case <-ctx.Done():
			return
		case networkDeviceDataList := <-p.networkDeviceDataUpdateChan:
			// select picks at random when both cases are ready.
			if ctx.Err() != nil {
				return
			}
			p.updateNetworkDeviceData(ctx, networkDeviceDataList)
		}
	}
}

// updateNetworkDeviceData persists the network data of each device in the
// checkpoint and records it on its claim, one claim at a time. This runs
// asynchronously so CNI ADD/DEL operations are not blocked by API retries.
func (p *Plugin) updateNetworkDeviceData(ctx context.Context, networkDataChanStructList types.NetworkDataChanStructList) {
	logger := klog.FromContext(ctx).WithName("updateNetworkDeviceData")
	logger.Info("Updating network device data", "networkDataChanStructList", networkDataChanStructList)

	groupedByClaim := p.groupNetworkDataByClaim(networkDataChanStructList)
	logger.V(2).Info("Grouped network updates by claim", "claimCount", len(groupedByClaim))
	for claimKey, claimUpdates := range groupedByClaim {
		updates := make([]types.DeviceNetworkStatus, 0, len(claimUpdates))
		for _, item := range claimUpdates {
			// A requeued update is stale once a later one for the device has
			// been recorded; the store holds whatever was recorded last.
			if item.Requeues > 0 && !apiequality.Semantic.DeepEqual(item.PreparedDevice.NetworkDeviceData, item.NetworkDeviceData) {
				logger.V(2).Info("Skipping requeued network data update superseded by a later one", "claim", claimKey.uid, "deviceName", item.PreparedDevice.Device.DeviceName)
				continue
			}
			if err := p.podManager.UpdatePreparedDeviceNetworkData(item.PreparedDevice, item.NetworkDeviceData); err != nil {
				logger.Error(err, "Failed to persist device network data before claim update", "claim", claimKey.uid, "deviceName", item.PreparedDevice.Device.DeviceName)
				continue
			}
			updates = append(updates, deviceNetworkStatus(logger, item))
		}
		if len(updates) == 0 {
			logger.V(2).Info("No claim status updates generated for claim", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			continue
		}

		err := p.updateClaimNetworkDataWithRetry(ctx, claimKey, updates)
		switch {
		case err == nil:
			logger.V(2).Info("Successfully updated claim network data", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
		case ctx.Err() != nil:
			logger.V(2).Info("Dropping claim network data update on shutdown", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
		case errors.Is(err, errClaimReleased), errors.Is(err, types.ErrDeviceNotAllocated), apierrors.IsNotFound(err):
			// The devices were prepared for a pod and claim that are gone; there
			// is nothing left for their network data to describe.
			logger.V(2).Info("Skipping claim the pod no longer holds", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace, "pod.UID", claimKey.podUID, "reason", err.Error())
		case types.IsPermanentStatusUpdateError(err):
			logger.Error(err, "Failed to update claim network data", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
		default:
			p.requeue(logger, claimKey, claimUpdates, err)
		}
	}
}

// requeue puts the updates of a claim whose status write failed on a
// transient error or repeated conflicts back on the queue, behind whatever is
// waiting, up to maxRequeues times.
func (p *Plugin) requeue(logger klog.Logger, claimKey networkClaimKey, claimUpdates types.NetworkDataChanStructList, cause error) {
	if claimUpdates[0].Requeues >= maxRequeues {
		logger.Error(cause, "Giving up on claim network data update", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace, "requeues", claimUpdates[0].Requeues)
		return
	}
	for _, item := range claimUpdates {
		item.Requeues++
	}
	select {
	case p.networkDeviceDataUpdateChan <- claimUpdates:
		logger.V(2).Info("Requeued claim network data update", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace, "requeues", claimUpdates[0].Requeues, "error", cause.Error())
	default:
		logger.Error(errUpdateQueueFull, "Dropping claim network data update", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace, "cause", cause.Error())
	}
}

// deviceNetworkStatus builds the status the runner records for one device.
// The retry replays it on every attempt, so it is built once and, unlike the
// event, shares nothing with the pod manager's PreparedDevice.
func deviceNetworkStatus(logger klog.Logger, item *types.NetworkDataChanStruct) types.DeviceNetworkStatus {
	// ShareID is left nil, PreparedDevice does not record one; the mutation
	// resolves it from the claim's allocation.
	status := types.DeviceNetworkStatus{
		Driver:      consts.DriverName,
		Pool:        item.PreparedDevice.Device.PoolName,
		Device:      item.PreparedDevice.Device.DeviceName,
		NetworkData: item.NetworkDeviceData.DeepCopy(),
	}
	combined := map[string]any{
		"vfConfig":  item.PreparedDevice.Config,
		"cniConfig": item.CNIConfig,
		"cniResult": item.CNIResult,
	}
	raw, err := json.Marshal(combined)
	if err != nil {
		logger.Error(err, "Failed to marshal device status data, leaving it unchanged", "deviceName", status.Device)
		return status
	}
	status.Data = &runtime.RawExtension{Raw: raw}
	return status
}

type networkClaimKey struct {
	namespace string
	name      string
	// uid is the claim this device was prepared for, so a claim recreated under
	// the same name is not mistaken for it.
	uid k8stypes.UID
	// podUID is the pod the device was prepared for; the claim must still be
	// reserved for it when its network data is written.
	podUID k8stypes.UID
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
			uid:       claim.UID,
			podUID:    k8stypes.UID(item.PreparedDevice.PodUID),
		}
		grouped[key] = append(grouped[key], item)
	}
	return grouped
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

// updateClaimNetworkDataWithRetry records updates on the claim, retrying on
// conflict. Only these devices are patched, on the latest claim, so entries
// other drivers or the prepare path wrote in the meantime survive. It shares
// the retry with the prepare path in pkg/driver.
func (p *Plugin) updateClaimNetworkDataWithRetry(ctx context.Context, claimKey networkClaimKey, updates []types.DeviceNetworkStatus) error {
	patch := types.PatchDeviceNetworkStatuses(updates)
	mutate := func(claim *resourceapi.ResourceClaim) (bool, error) {
		// Once the pod has released the claim its network data describes an
		// attachment that is gone, or one that belongs to the pod that has since
		// taken the claim over.
		if claimKey.podUID != "" && !slices.ContainsFunc(claim.Status.ReservedFor, func(consumer resourceapi.ResourceClaimConsumerReference) bool {
			return consumer.UID == claimKey.podUID
		}) {
			return false, errClaimReleased
		}
		return patch(claim)
	}
	backoff := p.statusBackoff
	if backoff.Steps == 0 {
		backoff = consts.Backoff
	}
	return types.UpdateClaimStatusWithRetry(
		ctx,
		p.k8sClient.ResourceV1().ResourceClaims(claimKey.namespace),
		claimKey.name,
		claimKey.uid,
		backoff,
		mutate,
	)
}
