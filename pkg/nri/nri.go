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
}

// NewNRIPlugin creates a new NRI plugin.
func NewNRIPlugin(config *types.Config, podManager *podmanager.PodManager, cniRuntime cni.Interface) (*Plugin, error) {
	p := &Plugin{
		podManager:                  podManager,
		cniRuntime:                  cniRuntime,
		k8sClient:                   config.K8sClient,
		interfacePrefix:             config.Flags.DefaultInterfacePrefix,
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

// updateNetworkDeviceData updates the network device data for each pod in the networkDataChanStructList.
// we use it so we don't block the CNI ADD/DEL operations as we are limited by the NRI plugin timeout
func (p *Plugin) updateNetworkDeviceData(ctx context.Context, networkDataChanStructList types.NetworkDataChanStructList) {
	logger := klog.FromContext(ctx).WithName("updateNetworkDeviceData")
	logger.Info("Updating network device data", "networkDataChanStructList", networkDataChanStructList)

	for _, networkDataChanStruct := range networkDataChanStructList {
		// get the claim object
		claim := &resourceapi.ResourceClaim{}
		err := p.k8sClient.Client.Get(ctx, client.ObjectKey{
			Name:      networkDataChanStruct.PreparedDevice.ClaimNamespacedName.Name,
			Namespace: networkDataChanStruct.PreparedDevice.ClaimNamespacedName.Namespace,
		}, claim)
		if err != nil {
			logger.Error(err, "Failed to get claim object", "claimName", networkDataChanStruct.PreparedDevice.ClaimNamespacedName.Name, "claimNamespace", networkDataChanStruct.PreparedDevice.ClaimNamespacedName.Namespace)
			continue
		}

		for idx, device := range claim.Status.Devices {
			if device.Device != networkDataChanStruct.PreparedDevice.Device.DeviceName || device.Pool != networkDataChanStruct.PreparedDevice.Device.PoolName || device.Driver != consts.DriverName {
				continue
			}
			claim.Status.Devices[idx].NetworkData = networkDataChanStruct.NetworkDeviceData

			// Build combined Data: { vfConfig, cniConfig, cniResult }
			combined := map[string]interface{}{
				"vfConfig":  networkDataChanStruct.PreparedDevice.Config,
				"cniConfig": networkDataChanStruct.CNIConfig,
				"cniResult": networkDataChanStruct.CNIResult,
			}
			raw, err := json.Marshal(combined)
			if err != nil {
				logger.V(2).Info("Failed to marshal combined Data, skipping Data update", "error", err.Error())
			} else {
				claim.Status.Devices[idx].Data = &runtime.RawExtension{Raw: raw}
			}
		}

		err = p.updateClaimNetworkDataWithRetry(ctx, claim)
		if err != nil {
			logger.Error(err, "Failed to update claim network data", "claim", claim.UID)
			continue
		}
	}
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
