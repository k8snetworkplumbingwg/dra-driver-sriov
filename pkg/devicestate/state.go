package devicestate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

type Manager struct {
	k8sClient              flags.ClientSets
	cdi                    *cdi.Handler
	defaultInterfacePrefix string
	allocatable            drasriovtypes.AllocatableDevices
	republishCallback      func(context.Context) error
}

func NewManager(config *drasriovtypes.Config, cdi *cdi.Handler) (*Manager, error) {
	allocatable, err := DiscoverSriovDevices()
	if err != nil {
		return nil, fmt.Errorf("error enumerating all possible devices: %v", err)
	}

	state := &Manager{
		k8sClient:              config.K8sClient,
		defaultInterfacePrefix: config.Flags.DefaultInterfacePrefix,
		cdi:                    cdi,
		allocatable:            allocatable,
	}

	return state, nil
}

// GetAllocatableDevices returns the allocatable devices
func (s *Manager) GetAllocatableDevices() drasriovtypes.AllocatableDevices {
	return s.allocatable
}

func (s *Manager) GetAllocatedDeviceByDeviceName(deviceName string) (resourceapi.Device, bool) {
	device, exist := s.allocatable[deviceName]
	return device, exist
}

// PrepareDevicesForClaim prepares the devices for a given claim
// It will return the prepared devices for the claim
func (s *Manager) PrepareDevicesForClaim(ctx context.Context, ifNameIndex *int, claim *resourceapi.ResourceClaim) (drasriovtypes.PreparedDevices, error) {
	logger := klog.FromContext(ctx).WithName("PrepareDevicesForClaim")

	resultsConfig, err := getMapOfOpaqueDeviceConfigForDevice(configapi.Decoder, claim.Status.Allocation.Devices.Config)
	if err != nil {
		logger.Error(err, "failed to create map of opaque device config for device", "claim", *claim)
		return nil, fmt.Errorf("error creating map of opaque device config for device: %v", err)
	}

	preparedDevices, err := s.prepareDevices(ctx, ifNameIndex, claim, resultsConfig)
	if err != nil {
		logger.Error(err, "Prepare failed", "claim", *claim)
		return nil, fmt.Errorf("prepare failed: %v", err)
	}
	if len(preparedDevices) == 0 {
		logger.Error(fmt.Errorf("no prepared devices found for claim"), "Prepare failed", "claim", *claim)
		return nil, fmt.Errorf("no prepared devices found for claim")
	}

	if err = s.cdi.CreateClaimSpecFile(preparedDevices); err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %v", err)
	}

	return preparedDevices, nil
}

func (s *Manager) prepareDevices(ctx context.Context, ifNameIndex *int,
	claim *resourceapi.ResourceClaim,
	resultsConfig map[string]*configapi.VfConfig) (drasriovtypes.PreparedDevices, error) {
	logger := klog.FromContext(ctx).WithName("prepareDevices")
	preparedDevices := drasriovtypes.PreparedDevices{}
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != consts.DriverName {
			continue
		}

		config, ok := resultsConfig[result.Request]
		if !ok {
			return nil, fmt.Errorf("config not found for request: %s", result.Request)
		}

		// make changes if needed
		config.Normalize()

		preparedDevice, err := s.applyConfigOnDevice(ctx, ifNameIndex, claim, config, &result)
		if err != nil {
			logger.Error(err, "error applying config on device", "config", config, "result", result)
			return nil, fmt.Errorf("error applying config on device: %v", err)
		}

		rawConfig, err := json.Marshal(config)
		if err != nil {
			logger.Error(err, "error marshaling config", "config", config)
			rawConfig = []byte("{}")
		}
		// Add applied config to device
		claim.Status.Devices = append(claim.Status.Devices, resourceapi.AllocatedDeviceStatus{
			Device: result.Device,
			Pool:   result.Pool,
			Driver: result.Driver,
			Data:   &runtime.RawExtension{Raw: rawConfig},
		})
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	logger.V(3).Info("Prepared devices", "preparedDevices", preparedDevices)
	return preparedDevices, nil
}

func (s *Manager) applyConfigOnDevice(ctx context.Context, ifNameIndex *int, claim *resourceapi.ResourceClaim, config *configapi.VfConfig, result *resourceapi.DeviceRequestAllocationResult) (*drasriovtypes.PreparedDevice, error) {
	logger := klog.FromContext(ctx).WithName("applyConfigOnDevice")
	logger.V(3).Info("Applying config on device", "config", config, "result", result)
	deviceInfo, exist := s.allocatable[result.Device]
	if !exist {
		return nil, fmt.Errorf("device %s not found in allocatable devices", result.Device)
	}

	netAttachDefNamespace := claim.GetNamespace()
	if config.NetAttachDefNamespace != "" {
		netAttachDefNamespace = config.NetAttachDefNamespace
	}

	netAttachDefRawConfig, err := s.getNetAttachDefRawConfig(ctx, netAttachDefNamespace, config.NetAttachDefName)
	if err != nil {
		return nil, fmt.Errorf("error getting net attach def raw config: %w", err)
	}
	// add to sriov-cni compatible netconf the deviceID (PCI address)
	pciAddress := *deviceInfo.Attributes[consts.AttributePciAddress].StringValue
	netAttachDefRawConfig, err = drasriovtypes.AddDeviceIDToNetConf(netAttachDefRawConfig, pciAddress)
	if err != nil {
		return nil, fmt.Errorf("error converting net attach def config to sriov-cni format: %w", err)
	}
	// Bind device to driver if specified in config
	originalDriver, err := host.GetHelpers().BindDeviceDriver(pciAddress, config)
	if err != nil {
		return nil, fmt.Errorf("error binding device %s to driver: %w", pciAddress, err)
	}

	// Ensure that the kernel module are loaded if the user request vhost mounts
	if config.AddVhostMount {
		if err := host.GetHelpers().EnsureVhostModulesLoaded(); err != nil {
			return nil, fmt.Errorf("failed to ensure vhost modules are loaded: %w", err)
		}
	}

	// create environment variables
	envs := []string{
		fmt.Sprintf("SRIOVNETWORK_VF_DEVICE_%s=%s", strings.ReplaceAll(result.Device, "-", "_"), *deviceInfo.Attributes[consts.AttributePciAddress].StringValue),
		fmt.Sprintf("SRIOVNETWORK_NET_ATTACH_DEF_NAME=%s", config.NetAttachDefName),
	}

	// Prepare device nodes slice for potential VFIO devices
	var deviceNodes []*cdispec.DeviceNode

	// If device is bound to vfio-pci, add VFIO device nodes
	if config.Driver == "vfio-pci" {
		devFileHost, devFileContainer, err := host.GetHelpers().GetVFIODeviceFile(pciAddress)
		if err != nil {
			return nil, fmt.Errorf("error getting VFIO device file for device %s: %w", pciAddress, err)
		}

		// Add VFIO device node
		deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
			Path:     devFileContainer,
			HostPath: devFileHost,
			Type:     "c", // character device
		})

		// Also add /dev/vfio/vfio (VFIO container device) if it exists
		vfioContainerPath := "/dev/vfio/vfio"
		deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
			Path:     vfioContainerPath,
			HostPath: vfioContainerPath,
			Type:     "c", // character device
		})

		envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_VFIO_DEVICE=%s", strings.ReplaceAll(result.Device, "-", "_"), devFileContainer))
		logger.V(2).Info("Added VFIO device nodes for device", "device", pciAddress, "hostPath", devFileHost, "containerPath", devFileContainer)
	}

	// if addVhostMount is true, we add a volume mount for the vhost device
	if config.AddVhostMount {
		deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
			Path:     "/dev/vhost-net",
			HostPath: "/dev/vhost-net",
			Type:     "c", // character device
		})
		deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
			Path:     "/dev/net/tun",
			HostPath: "/dev/net/tun",
			Type:     "c", // character device
		})
	}

	edits := &cdispec.ContainerEdits{
		Env:         envs,
		DeviceNodes: deviceNodes,
	}

	ifName := config.IfName
	// if the device name is not set, we use the default interface prefix
	// and the interface index, we also bump the index.
	if ifName == "" {
		ifName = fmt.Sprintf("%s%d", s.defaultInterfacePrefix, *ifNameIndex)
		*ifNameIndex++
	}

	preparedDevice := &drasriovtypes.PreparedDevice{
		ClaimNamespacedName: kubeletplugin.NamespacedObject{
			NamespacedName: k8stypes.NamespacedName{
				Name:      claim.Name,
				Namespace: claim.Namespace,
			},
			UID: claim.UID,
		},
		Device: drapbv1.Device{
			RequestNames: []string{result.Request},
			PoolName:     result.Pool,
			DeviceName:   result.Device,
			CDIDeviceIDs: []string{s.cdi.GetClaimDevices(string(claim.UID), result.Device), s.cdi.GetPodSpecName(string(claim.Status.ReservedFor[0].UID))},
		},
		ContainerEdits:     &cdiapi.ContainerEdits{ContainerEdits: edits},
		NetAttachDefConfig: netAttachDefRawConfig,
		IfName:             ifName,
		PciAddress:         pciAddress,
		PodUID:             string(claim.Status.ReservedFor[0].UID),
		Config:             config,
		OriginalDriver:     originalDriver,
	}

	return preparedDevice, nil
}

func (s *Manager) getNetAttachDefRawConfig(ctx context.Context, namespace string, netAttachDefName string) (string, error) {
	// Get the net attach def information
	netAttachDef := &netattdefv1.NetworkAttachmentDefinition{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{
		Name:      netAttachDefName,
		Namespace: namespace,
	}, netAttachDef)
	if err != nil {
		return "", fmt.Errorf("error getting net attach def for net attach def %s/%s: %w", namespace, netAttachDefName, err)
	}
	return netAttachDef.Spec.Config, nil
}

func (s *Manager) Unprepare(claimUID string, preparedDevices drasriovtypes.PreparedDevices) error {
	if err := s.unprepareDevices(preparedDevices); err != nil {
		return fmt.Errorf("unprepare failed: %v", err)
	}

	err := s.cdi.DeleteSpecFile(claimUID)
	if err != nil {
		return fmt.Errorf("unable to delete CDI spec file for PodUID: %v", err)
	}

	err = s.cdi.DeleteSpecFile(preparedDevices[0].PodUID)
	if err != nil {
		return fmt.Errorf("unable to delete CDI spec file for PodUID: %v", err)
	}

	return nil
}

// unprepareDevices reverts the driver configuration for the prepared devices
func (s *Manager) unprepareDevices(preparedDevices drasriovtypes.PreparedDevices) error {
	logger := klog.FromContext(context.Background()).WithName("unprepareDevices")
	for _, preparedDevice := range preparedDevices {
		// Restore original driver if a driver change was made
		if preparedDevice.Config.Driver != "" {
			if err := host.GetHelpers().RestoreDeviceDriver(preparedDevice.PciAddress, preparedDevice.OriginalDriver); err != nil {
				klog.Error(err, "Failed to restore original driver for device", "device", preparedDevice.PciAddress, "originalDriver", preparedDevice.OriginalDriver)
				return fmt.Errorf("failed to restore original driver for device %s: %w", preparedDevice.PciAddress, err)
			}
			logger.V(2).Info("Successfully restored original driver for device", "device", preparedDevice.PciAddress, "originalDriver", preparedDevice.OriginalDriver)
		}
	}
	return nil
}

// UpdateDeviceResourceNames updates the resource names for devices and triggers a republish
// deviceResourceMap is a map of device name to resource name. Empty resource name removes the attribute.
func (s *Manager) UpdateDeviceResourceNames(ctx context.Context, deviceResourceMap map[string]string) error {
	logger := klog.FromContext(ctx).WithName("UpdateDeviceResourceNames")
	logger.V(2).Info("Updating device resource names", "deviceCount", len(deviceResourceMap))

	// Track if any changes were made
	changesMade := false

	// Update allocatable devices with resource names
	for deviceName, resourceName := range deviceResourceMap {
		if device, exists := s.allocatable[deviceName]; exists {
			// Add or update the resource name attribute
			if resourceName != "" {
				// Set resource name
				if device.Attributes == nil {
					device.Attributes = make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute)
				}

				// Check if attribute already exists with the same value
				if existingAttr, exists := device.Attributes[consts.AttributeResourceName]; !exists ||
					existingAttr.StringValue == nil || *existingAttr.StringValue != resourceName {
					device.Attributes[consts.AttributeResourceName] = resourceapi.DeviceAttribute{
						StringValue: &resourceName,
					}
					s.allocatable[deviceName] = device
					changesMade = true
					logger.V(3).Info("Set resource name for device", "deviceName", deviceName, "resourceName", resourceName)
				}
			} else {
				// Remove resource name attribute if it exists
				if _, exists := device.Attributes[consts.AttributeResourceName]; exists {
					delete(device.Attributes, consts.AttributeResourceName)
					s.allocatable[deviceName] = device
					changesMade = true
					logger.V(3).Info("Cleared resource name for device", "deviceName", deviceName)
				}
			}
		} else {
			logger.V(2).Info("Device not found in allocatable devices", "deviceName", deviceName)
		}
	}

	// Clear resource name attribute for devices not in the map
	for deviceName, device := range s.allocatable {
		if _, inMap := deviceResourceMap[deviceName]; !inMap {
			if _, exists := device.Attributes[consts.AttributeResourceName]; exists {
				delete(device.Attributes, consts.AttributeResourceName)
				s.allocatable[deviceName] = device
				changesMade = true
				logger.V(3).Info("Cleared resource name for device not in filter", "deviceName", deviceName)
			}
		}
	}

	if changesMade {
		logger.Info("Device resource names updated", "totalDevices", len(s.allocatable), "filteredDevices", len(deviceResourceMap))

		// Trigger resource republishing if callback is available
		if s.republishCallback != nil {
			if err := s.republishCallback(ctx); err != nil {
				logger.Error(err, "Failed to republish resources after updating resource names")
				return fmt.Errorf("failed to republish resources: %w", err)
			}
			logger.V(2).Info("Successfully republished resources after updating resource names")
		} else {
			logger.V(2).Info("No republish callback available - resources will be updated on next periodic refresh")
		}
	} else {
		logger.V(2).Info("No changes made to device resource names")
	}

	return nil
}

// SetRepublishCallback sets the callback function to trigger resource republishing
func (s *Manager) SetRepublishCallback(callback func(context.Context) error) {
	s.republishCallback = callback
}
