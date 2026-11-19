package nri

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cni"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// Plugin represents a NRI plugin catching RunPodSandbox and StopPodSandbox events to
// call CNI ADD/DEL based on ResourceClaim attached to pods.
type Plugin struct {
	stub       stub.Stub
	podManager types.DeviceStore
	cniRuntime cni.Interface

	k8sClient            flags.ClientSets
	interfacePrefix      string
	enableDeviceMetadata bool
	metadataUpdater      types.MetadataUpdater
	// statusBackoff paces claim status retries; zero means consts.Backoff.
	statusBackoff wait.Backoff

	// networkDeviceDataUpdateChan is never closed, since a hook may still send to it after Stop.
	networkDeviceDataUpdateChan chan types.NetworkDataChanStructList
	// networkDataSeq numbers observations, so a requeued update is not taken for a later one.
	networkDataSeq atomic.Uint64
	runnerMu       sync.Mutex
	runnerCancel   context.CancelFunc
	runnerDone     chan struct{}
}

var (
	errUpdateQueueFull = errors.New("network data update queue is full")
	errClaimReleased   = errors.New("claim is no longer reserved for the pod")
)

// maxRequeues is how often a failed claim status update is requeued before it is dropped.
const maxRequeues = 3

// NewNRIPlugin creates a new NRI plugin.
func NewNRIPlugin(config *types.Config, podManager types.DeviceStore, cniRuntime cni.Interface, metadataUpdater types.MetadataUpdater) (*Plugin, error) {
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

// Stop stops the stub, so no new hooks arrive, then the runner. Queued updates are dropped.
func (p *Plugin) Stop() {
	p.stub.Stop()
	p.stopRunner()
}

// startRunner runs updateNetworkDeviceDataRunner until ctx ends or stopRunner is called.
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

// stopRunner cancels the runner, dropping its backlog, and waits for it to
// return; a second caller waits too. Before startRunner it does nothing.
func (p *Plugin) stopRunner() {
	p.runnerMu.Lock()
	defer p.runnerMu.Unlock()
	if p.runnerCancel == nil {
		return
	}
	p.runnerCancel()
	<-p.runnerDone
	p.runnerCancel, p.runnerDone = nil, nil
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
			Seq:               p.networkDataSeq.Add(1),
		})
		logger.Info("Attached network", "deviceName", device.Device.DeviceName, "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace, "networkDeviceData", networkDeviceData)
	}

	// Refresh request metadata synchronously so runtime CNI fields are available
	// when the pod starts.
	if err := p.updateRequestMetadataBeforeSandboxStart(ctx, networkDevicesData); err != nil {
		logger.Error(err, "Failed to update request metadata before pod start", "pod.UID", pod.Uid, "pod.Name", pod.Name, "pod.Namespace", pod.Namespace)
		return fmt.Errorf("failed to update request metadata before pod start: %w", err)
	}

	// Updates run asynchronously to keep the hook within its timeout. When the
	// queue is full, drop the update rather than stall sandbox creation.
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

// updateNetworkDeviceData records each device's network data in the checkpoint
// and on its claim, one claim at a time, off the NRI hook.
func (p *Plugin) updateNetworkDeviceData(ctx context.Context, networkDataChanStructList types.NetworkDataChanStructList) {
	logger := klog.FromContext(ctx).WithName("updateNetworkDeviceData")
	logger.Info("Updating network device data", "networkDataChanStructList", networkDataChanStructList)

	groupedByClaim := p.groupNetworkDataByClaim(networkDataChanStructList)
	logger.V(2).Info("Grouped network updates by claim", "claimCount", len(groupedByClaim))
	for claimKey, claimUpdates := range groupedByClaim {
		if ctx.Err() != nil {
			logger.V(2).Info("Dropping remaining claim network data updates on shutdown", "claim", claimKey.uid)
			return
		}
		updates := make([]resourceapi.AllocatedDeviceStatus, 0, len(claimUpdates))
		// Updates that never reached the checkpoint are requeued on their own.
		var unrecorded types.NetworkDataChanStructList
		var unrecordedErr error
		for _, item := range claimUpdates {
			if ctx.Err() != nil {
				logger.V(2).Info("Dropping the rest of the claim's network data updates on shutdown", "claim", claimKey.uid)
				return
			}
			// A later observation of the device was recorded, so this one is stale.
			if item.PreparedDevice.NetworkDataSeq > item.Seq {
				logger.V(2).Info("Skipping network data update superseded by a later observation", "claim", claimKey.uid, "deviceName", item.PreparedDevice.Device.DeviceName)
				continue
			}
			if !item.Checkpointed {
				if err := p.podManager.UpdatePreparedDeviceNetworkData(item.PreparedDevice, item.NetworkDeviceData, item.Seq); err != nil {
					logger.Error(err, "Failed to persist device network data before claim update", "claim", claimKey.uid, "deviceName", item.PreparedDevice.Device.DeviceName)
					unrecorded = append(unrecorded, item)
					unrecordedErr = err
					continue
				}
				item.Checkpointed = true
			}
			updates = append(updates, deviceNetworkStatus(logger, item))
		}
		if len(updates) == 0 {
			logger.V(2).Info("No claim status updates generated for claim", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			p.requeue(logger, claimKey, unrecorded, unrecordedErr)
			continue
		}

		err := p.updateClaimNetworkDataWithRetry(ctx, claimKey, updates)
		switch {
		case err == nil:
			logger.V(2).Info("Successfully updated claim network data", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			p.requeue(logger, claimKey, unrecorded, unrecordedErr)
		case ctx.Err() != nil:
			logger.V(2).Info("Dropping claim network data update on shutdown", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			return
		case errors.Is(err, errClaimReleased), errors.Is(err, types.ErrDeviceNotAllocated), apierrors.IsNotFound(err):
			// The pod or the claim is gone, so the network data describes nothing.
			logger.V(2).Info("Skipping claim the pod no longer holds", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace, "pod.UID", claimKey.podUID, "reason", err.Error())
		case types.IsPermanentStatusUpdateError(err):
			// The refused write does not cancel what is still owed to the checkpoint.
			logger.Error(err, "Failed to update claim network data", "claim", claimKey.uid, "claimName", claimKey.name, "claimNamespace", claimKey.namespace)
			p.requeue(logger, claimKey, unrecorded, unrecordedErr)
		default:
			p.requeue(logger, claimKey, claimUpdates, err)
		}
	}
}

// requeue puts the updates a claim still owes back on the queue, up to
// maxRequeues times. The first update carries the count for the batch.
func (p *Plugin) requeue(logger klog.Logger, claimKey networkClaimKey, claimUpdates types.NetworkDataChanStructList, cause error) {
	if len(claimUpdates) == 0 {
		return
	}
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

// deviceNetworkStatus builds the status recorded for one device. The retry
// replays it, so it shares no memory with the PreparedDevice.
func deviceNetworkStatus(logger klog.Logger, item *types.NetworkDataChanStruct) resourceapi.AllocatedDeviceStatus {
	// ShareID matches a device allocated more than once to its own share.
	status := resourceapi.AllocatedDeviceStatus{
		Driver:      consts.DriverName,
		Pool:        item.PreparedDevice.Device.PoolName,
		Device:      item.PreparedDevice.Device.DeviceName,
		ShareID:     item.PreparedDevice.ShareID,
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
	// podUID is the pod the device was prepared for; the claim must still be reserved for it.
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

// updateClaimNetworkDataWithRetry patches these devices on the latest claim,
// retrying on conflict, so entries others wrote meanwhile survive.
func (p *Plugin) updateClaimNetworkDataWithRetry(ctx context.Context, claimKey networkClaimKey, updates []resourceapi.AllocatedDeviceStatus) error {
	patch := types.PatchDeviceNetworkStatuses(updates)
	mutate := func(claim *resourceapi.ResourceClaim) (bool, error) {
		// Once the pod released the claim, its network data no longer applies.
		if claimKey.podUID != "" && !slices.ContainsFunc(claim.Status.ReservedFor, func(consumer resourceapi.ResourceClaimConsumerReference) bool {
			return consumer.UID == claimKey.podUID
		}) {
			return false, errClaimReleased
		}
		return patch(claim)
	}
	return types.UpdateClaimStatusWithRetry(
		ctx,
		p.k8sClient.ResourceV1().ResourceClaims(claimKey.namespace),
		claimKey.name,
		claimKey.uid,
		cmp.Or(p.statusBackoff, consts.Backoff),
		mutate,
	)
}
