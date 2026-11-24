package devicestate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/containernetworking/cni/libcni"
	nadutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// Manager tracks discovered SR-IOV devices and manages claim prepare/unprepare lifecycle.
type Manager struct {
	k8sClient              flags.ClientSets
	cdi                    *cdi.Handler
	deviceInfoStore        DeviceInfoStore
	defaultInterfacePrefix string
	allocatable            drasriovtypes.AllocatableDevices
	republishCallback      func(context.Context) error
	// policyAttrKeys tracks attribute keys set by policy per device, so they
	// can be cleared without touching discovery attributes. Presence of a
	// device key also indicates that the device is advertised (policy-matched).
	policyAttrKeys    map[string]map[resourceapi.QualifiedName]bool
	configurationMode string
	nodeName          string
	iommuAvailable    bool
}

// NewManager validates the networking mode and discovers the node's existing VFs.
func NewManager(config *drasriovtypes.Config, cdi *cdi.Handler, deviceInfoStore DeviceInfoStore) (*Manager, error) {
	if config == nil {
		return nil, fmt.Errorf("config must not be nil")
	}
	if config.Flags == nil {
		return nil, fmt.Errorf("config flags must not be nil")
	}
	if cdi == nil {
		return nil, fmt.Errorf("cdi handler must not be nil")
	}

	configurationMode, err := NormalizeConfigurationMode(config.Flags.ConfigurationMode)
	if err != nil {
		return nil, err
	}

	allocatable, err := DiscoverSriovDevices()
	if err != nil {
		return nil, fmt.Errorf("error enumerating all possible devices: %v", err)
	}

	if deviceInfoStore == nil {
		deviceInfoStore = NewDeviceInfoStore()
	}

	state := &Manager{
		k8sClient:              config.K8sClient,
		defaultInterfacePrefix: config.Flags.DefaultInterfacePrefix,
		cdi:                    cdi,
		deviceInfoStore:        deviceInfoStore,
		allocatable:            allocatable,
		configurationMode:      configurationMode,
		nodeName:               config.Flags.NodeName,
		iommuAvailable:         host.GetHelpers().IsIommufdAvailable(),
	}

	return state, nil
}

// GetAllocatableDevices returns the allocatable devices
func (s *Manager) GetAllocatableDevices() drasriovtypes.AllocatableDevices {
	return s.allocatable
}

// NormalizeConfigurationMode defaults an empty mode to STANDALONE and rejects unsupported values.
func NormalizeConfigurationMode(mode string) (string, error) {
	switch consts.ConfigurationMode(mode) {
	case "":
		return string(consts.ConfigurationModeStandalone), nil
	case consts.ConfigurationModeStandalone:
		return string(consts.ConfigurationModeStandalone), nil
	case consts.ConfigurationModeMultus:
		return string(consts.ConfigurationModeMultus), nil
	default:
		return "", fmt.Errorf("unsupported configuration mode %q, expected %q or %q", mode, consts.ConfigurationModeStandalone, consts.ConfigurationModeMultus)
	}
}

// GetAllocatableDeviceByName returns a discovered allocatable device and whether it exists.
func (s *Manager) GetAllocatableDeviceByName(deviceName string) (resourceapi.Device, bool) {
	device, exists := s.allocatable[deviceName]
	return device, exists
}

// isStandaloneMode reports whether the manager is running in STANDALONE mode.
func (s *Manager) isStandaloneMode() bool {
	mode := consts.ConfigurationMode(s.configurationMode)
	return mode == "" || mode == consts.ConfigurationModeStandalone
}

// isMultusMode reports whether the manager is running in MULTUS mode.
func (s *Manager) isMultusMode() bool {
	return consts.ConfigurationMode(s.configurationMode) == consts.ConfigurationModeMultus
}

// PrepareDevicesForClaim preflights and prepares devices from trusted resolved configuration.
func (s *Manager) PrepareDevicesForClaim(
	ctx context.Context,
	ifNameIndex *int,
	claim *resourceapi.ResourceClaim,
	resolved *ResolvedClaimConfiguration,
) (drasriovtypes.PreparedDevices, error) {
	logger := klog.FromContext(ctx).WithName("PrepareDevicesForClaim")
	if resolved == nil {
		return nil, fmt.Errorf("resolved claim configuration is required")
	}

	preparedDevices, err := s.prepareDevices(ctx, ifNameIndex, claim, resolved)
	if err != nil {
		logger.Error(err, "Prepare failed", "claim", klog.KObj(claim))
		return nil, fmt.Errorf("prepare failed: %v", err)
	}
	if len(preparedDevices) == 0 {
		logger.Error(fmt.Errorf("no prepared devices found for claim"), "Prepare failed", "claim", klog.KObj(claim))
		return nil, fmt.Errorf("no prepared devices found for claim")
	}

	if err = s.syncDeviceInfoFilesForPreparedDevicesIfNeeded(ctx, preparedDevices); err != nil {
		rollbackErrs := []error{fmt.Errorf("unable to create device-info files for claim: %v", err)}
		if cleanupErr := s.cleanDeviceInfoFilesForPreparedDevicesIfNeeded(ctx, preparedDevices); cleanupErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("cleanup after device-info sync failure failed: %w", cleanupErr))
		}
		if rollbackErr := s.unprepareDevices(preparedDevices); rollbackErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return nil, errors.Join(rollbackErrs...)
	}

	if err = s.cdi.CreateClaimSpecFile(preparedDevices); err != nil {
		rollbackErrs := []error{fmt.Errorf("unable to create CDI spec file for claim: %v", err)}
		if cleanupErr := s.cleanDeviceInfoFilesForPreparedDevicesIfNeeded(ctx, preparedDevices); cleanupErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("cleanup after CDI spec failure failed: %w", cleanupErr))
		}
		if rollbackErr := s.unprepareDevices(preparedDevices); rollbackErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return nil, errors.Join(rollbackErrs...)
	}

	return preparedDevices, nil
}

// prepareDevices preflights the claim, applies independent configurations, and records device status.
func (s *Manager) prepareDevices(ctx context.Context, ifNameIndex *int,
	claim *resourceapi.ResourceClaim,
	resolved *ResolvedClaimConfiguration,
) (drasriovtypes.PreparedDevices, error) {
	logger := klog.FromContext(ctx).WithName("prepareDevices")
	inputs, err := s.preflightDevices(ctx, ifNameIndex, claim, resolved)
	if err != nil {
		return nil, err
	}
	preparedDevices := drasriovtypes.PreparedDevices{}
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != consts.DriverName {
			continue
		}

		key := allocationKeyFromResult(&result)
		resolvedDev, ok := resolved.ByKey[key]
		if !ok {
			return nil, fmt.Errorf("no resolved configuration for allocation key %s", key)
		}
		config := resolvedDev.Config.DeepCopy()
		config.Normalize()

		preparedDevice, err := s.applyConfigOnDevice(ctx, claim, config, &result, inputs[key])
		if err != nil {
			logger.Error(err, "error applying config on device", "config", config, "result", result)
			if rollbackErr := s.unprepareDevices(preparedDevices); rollbackErr != nil {
				return nil, fmt.Errorf("error applying config on device: %v; rollback failed: %v", err, rollbackErr)
			}
			return nil, fmt.Errorf("error applying config on device: %v", err)
		}

		rawConfig, err := json.Marshal(config)
		if err != nil {
			logger.Error(err, "error marshaling config", "config", config)
			rawConfig = []byte("{}")
		}
		// Record the applied config as this device's status. Upsert instead of
		// append: a claim reused by another pod, or re-prepared after a restart,
		// can already carry this device, and a duplicate (driver, pool, device,
		// share ID) key makes the whole status update fail validation.
		claim.Status.Devices = drasriovtypes.UpsertDeviceStatus(claim.Status.Devices, resourceapi.AllocatedDeviceStatus{
			Device:  result.Device,
			Pool:    result.Pool,
			Driver:  result.Driver,
			ShareID: (*string)(result.ShareID),
			Data:    &runtime.RawExtension{Raw: rawConfig},
		})
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	logger.V(3).Info("Prepared devices", "preparedDevices", preparedDevices)
	return preparedDevices, nil
}

// preflightDevice validates a local allocation and resolves its standalone NAD before device changes.
func (s *Manager) preflightDevice(ctx context.Context, config *configapi.VfConfig, result *resourceapi.DeviceRequestAllocationResult) (string, error) {
	deviceInfo, exists := s.allocatable[result.Device]
	if !exists {
		return "", fmt.Errorf("device %s not found in allocatable devices", result.Device)
	}
	if s.nodeName != "" && result.Pool != s.nodeName {
		return "", fmt.Errorf("allocation pool %q does not match local pool %q", result.Pool, s.nodeName)
	}
	mode, err := NormalizeConfigurationMode(s.configurationMode)
	if err != nil {
		return "", err
	}
	if err := configapi.ValidateForPrepare(mode, config); err != nil {
		return "", err
	}
	var netAttachDefRawConfig string
	pciAttr := deviceInfo.Attributes[consts.AttributePciAddress]
	if pciAttr.StringValue == nil || *pciAttr.StringValue == "" {
		return "", fmt.Errorf("device %s has no PCI address", result.Device)
	}
	pciAddress := *pciAttr.StringValue
	if s.isStandaloneMode() && (config.NetAttachDefName != "" || config.NetAttachDefNamespace != "") {
		netAttachDefRawConfig, err = s.getNetAttachDefRawConfig(ctx, config.NetAttachDefNamespace, config.NetAttachDefName)
		if err != nil {
			return "", fmt.Errorf("error getting net attach def raw config: %w", err)
		}
		netAttachDefRawConfig, err = drasriovtypes.AddDeviceIDToNetConf(netAttachDefRawConfig, pciAddress)
		if err != nil {
			return "", fmt.Errorf("error converting net attach def config to sriov-cni format: %w", err)
		}
	}

	if netAttachDefRawConfig != "" {
		raw, err := nadutils.GetCNIConfigFromSpec(netAttachDefRawConfig, consts.DriverName)
		if err != nil {
			return "", fmt.Errorf("invalid NAD configuration: %w", err)
		}
		if _, err := libcni.NetworkPluginConfFromBytes(raw); err != nil {
			return "", fmt.Errorf("invalid NAD configuration: %w", err)
		}
	}
	return netAttachDefRawConfig, nil
}

type devicePreparationInput struct{ networkConfig, ifName string }

// preflightDevices validates devices and assigns interface names without changing trusted configuration.
func (s *Manager) preflightDevices(ctx context.Context, ifNameIndex *int, claim *resourceapi.ResourceClaim, resolved *ResolvedClaimConfiguration) (map[AllocationKey]devicePreparationInput, error) {
	inputs := make(map[AllocationKey]devicePreparationInput, len(resolved.ByKey))
	used := make(map[string]bool, len(resolved.ExistingIfNames))
	for name, owner := range resolved.ExistingIfNames {
		if owner != claim.UID {
			used[name] = true
		}
	}
	// Reserve every fixed name before assigning any automatic name.
	for _, entry := range resolved.ByKey {
		if name := entry.Config.IfName; name != "" {
			if used[name] {
				return nil, fmt.Errorf("interface name %q already used in this Pod", name)
			}
			used[name] = true
		}
	}
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != consts.DriverName {
			continue
		}
		key := allocationKeyFromResult(&result)
		entry, ok := resolved.ByKey[key]
		if !ok {
			return nil, fmt.Errorf("no resolved configuration for allocation key %s", key)
		}
		networkConfig, err := s.preflightDevice(ctx, entry.Config, &result)
		if err != nil {
			return nil, fmt.Errorf("request %q device %s: %w", result.Request, result.Device, err)
		}
		name := entry.Config.IfName
		if s.isStandaloneMode() && name == "" && networkConfig != "" {
			for {
				name = fmt.Sprintf("%s%d", s.defaultInterfacePrefix, *ifNameIndex)
				*ifNameIndex++
				if !used[name] {
					break
				}
			}
			if err := configapi.ValidateInterfaceName(name); err != nil {
				return nil, err
			}
			used[name] = true
		}
		inputs[key] = devicePreparationInput{networkConfig: networkConfig, ifName: name}
	}
	return inputs, nil
}

// applyConfigOnDevice binds one preflighted device and builds its applied and cleanup state.
func (s *Manager) applyConfigOnDevice(ctx context.Context, claim *resourceapi.ResourceClaim, config *configapi.VfConfig, result *resourceapi.DeviceRequestAllocationResult, input devicePreparationInput) (*drasriovtypes.PreparedDevice, error) {
	logger := klog.FromContext(ctx).WithName("applyConfigOnDevice")
	logger.V(3).Info("Applying config on device", "config", config, "result", result)
	deviceInfo, exist := s.allocatable[result.Device]
	if !exist {
		return nil, fmt.Errorf("device %s not found in allocatable devices", result.Device)
	}
	// if in multus mode, we try to get the multus resource name and device ID from the device attributes
	var multusResourceName string
	var multusDeviceID string
	if s.isMultusMode() {
		var hasMultusDeviceInfo bool
		multusResourceName, multusDeviceID, hasMultusDeviceInfo = extractMultusDeviceInfoAttrs(logger, deviceInfo.Attributes)
		if !hasMultusDeviceInfo {
			logger.V(2).Info("Required Multus device-info attributes are missing or invalid for this allocation; device-info file generation will be skipped for this device",
				"deviceName", result.Device)
		}
	}

	pciAddress := *deviceInfo.Attributes[consts.AttributePciAddress].StringValue
	// Bind device to driver if specified in config
	originalDriver, err := host.GetHelpers().BindDeviceDriver(pciAddress, config)
	if err != nil {
		return nil, fmt.Errorf("error binding device %s to driver: %w", pciAddress, err)
	}
	restoreDriverOnError := func(cause error) error {
		if config.Driver == "" {
			return cause
		}
		if restoreErr := host.GetHelpers().RestoreDeviceDriver(pciAddress, originalDriver); restoreErr != nil {
			return fmt.Errorf("%w; additionally failed to restore original driver for device %s: %v", cause, pciAddress, restoreErr)
		}
		return cause
	}

	// Ensure that the kernel module are loaded if the user request vhost mounts
	if config.AddVhostMount {
		if err := host.GetHelpers().EnsureVhostModulesLoaded(); err != nil {
			return nil, restoreDriverOnError(fmt.Errorf("failed to ensure vhost modules are loaded: %w", err))
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
			return nil, restoreDriverOnError(fmt.Errorf("error getting VFIO device file for device %s: %w", pciAddress, err))
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

		// Add VFIO cdev device node and /dev/iommu for iommufd-capable kernels.
		// The cdev is only useful to a workload when /dev/iommu is also present,
		// so both are gated on iommuAvailable, which is checked once at driver
		// startup (its value only changes on host reboot, not per allocation).
		var cdevPath string
		if s.iommuAvailable {
			cdevPath, err = host.GetHelpers().GetVFIOCdevPath(pciAddress)
			if err != nil {
				return nil, restoreDriverOnError(fmt.Errorf("error getting VFIO cdev for device %s: %w", pciAddress, err))
			}
			if cdevPath != "" {
				deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
					Path:     cdevPath,
					HostPath: cdevPath,
					Type:     "c",
				})
			}

			deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
				Path:     "/dev/iommu",
				HostPath: "/dev/iommu",
				Type:     "c",
			})
		}

		envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_VFIO_DEVICE=%s", strings.ReplaceAll(result.Device, "-", "_"), devFileContainer))
		logger.V(2).Info("Added VFIO device nodes for device", "device", pciAddress,
			"hostPath", devFileHost, "containerPath", devFileContainer,
			"cdevPath", cdevPath, "iommuAvailable", s.iommuAvailable)
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

	// Add RDMA character devices if applicable
	rdmaDeviceNodes, rdmaEnvs, err := s.handleRDMADevice(ctx, deviceInfo, pciAddress, result.Device)
	if err != nil {
		return nil, restoreDriverOnError(fmt.Errorf("error handling RDMA device: %w", err))
	}
	deviceNodes = append(deviceNodes, rdmaDeviceNodes...)
	envs = append(envs, rdmaEnvs...)

	edits := &cdispec.ContainerEdits{
		Env:         envs,
		DeviceNodes: deviceNodes,
	}

	ifName := input.ifName

	metadataAttributes := buildMetadataAttributes(deviceInfo.Attributes, ifName)

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
			CdiDeviceIds: []string{s.cdi.GetClaimDevices(string(claim.UID), result.Device), s.cdi.GetPodSpecName(string(claim.Status.ReservedFor[0].UID))},
		},
		ContainerEdits:     &cdiapi.ContainerEdits{ContainerEdits: edits},
		NetAttachDefConfig: input.networkConfig,
		IfName:             ifName,
		PciAddress:         pciAddress,
		MultusDeviceID:     multusDeviceID,
		MultusResourceName: multusResourceName,
		DeviceAttributes:   metadataAttributes,
		PodUID:             string(claim.Status.ReservedFor[0].UID),
		Config:             config,
		OriginalDriver:     originalDriver,
		ShareID:            allocationKeyFromResult(result).ShareID,
	}

	return preparedDevice, nil
}

// handleRDMADevice handles RDMA device configuration and returns device nodes, environment variables, or an error
func (s *Manager) handleRDMADevice(ctx context.Context, deviceInfo resourceapi.Device, pciAddress, deviceName string) ([]*cdispec.DeviceNode, []string, error) {
	logger := klog.FromContext(ctx).WithName("handleRDMADevice")

	// Check if device is RDMA capable
	if rdmaCapableAttr, ok := deviceInfo.Attributes[consts.AttributeRDMACapable]; !ok || rdmaCapableAttr.BoolValue == nil || !*rdmaCapableAttr.BoolValue {
		return nil, nil, nil
	}

	var deviceNodes []*cdispec.DeviceNode
	var envs []string

	rdmaDevices := host.GetHelpers().GetRDMADevicesForPCI(pciAddress)

	if len(rdmaDevices) == 0 {
		logger.V(2).Info("No RDMA devices found for PCI address (device may be bound to vfio-pci)", "device", pciAddress)
		return nil, nil, nil
	}

	if len(rdmaDevices) > 1 {
		return nil, nil, fmt.Errorf("expected exactly one RDMA device for PCI address %s, but found %d: %v", pciAddress, len(rdmaDevices), rdmaDevices)
	}

	rdmaDevice := rdmaDevices[0]
	logger.V(2).Info("Device is RDMA capable, adding RDMA character devices",
		"device", pciAddress, "rdmaDevice", rdmaDevice)

	// Get character devices for this RDMA device
	charDevices, err := host.GetHelpers().GetRDMACharDevices(rdmaDevice)
	if err != nil {
		logger.Error(err, "Failed to get RDMA character devices",
			"device", pciAddress, "rdmaDevice", rdmaDevice)
		return nil, nil, err
	}

	if len(charDevices) == 0 {
		logger.V(2).Info("No RDMA character devices found",
			"device", pciAddress, "rdmaDevice", rdmaDevice)
		return nil, nil, fmt.Errorf("no RDMA character devices found for RDMA device %s (PCI: %s)", rdmaDevice, pciAddress)
	}

	// Use RDMA device name in env var key to support multiple RDMA devices
	devicePrefix := strings.ReplaceAll(deviceName, "-", "_")

	// Add each character device to the CDI spec
	for _, charDev := range charDevices {
		deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
			Path:     charDev,
			HostPath: charDev,
			Type:     "c", // character device
		})

		// Add environment variable for each character device type
		// Include RDMA device name to avoid collisions with multiple RDMA devices
		switch {
		case strings.HasPrefix(filepath.Base(charDev), "uverbs"):
			envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_RDMA_UVERB=%s", devicePrefix, charDev))
		case strings.HasPrefix(filepath.Base(charDev), "umad"):
			envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_RDMA_UMAD=%s", devicePrefix, charDev))
		case strings.HasPrefix(filepath.Base(charDev), "issm"):
			envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_RDMA_ISSM=%s", devicePrefix, charDev))
		case filepath.Base(charDev) == "rdma_cm":
			envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_RDMA_CM=%s", devicePrefix, charDev))
		}
	}

	logger.V(2).Info("Added RDMA character devices for device",
		"device", pciAddress, "rdmaDevice", rdmaDevice, "charDevices", charDevices, "envs", envs)

	// Add RDMA device name to environment variables
	envs = append(envs, fmt.Sprintf("SRIOVNETWORK_%s_RDMA_DEVICE=%s",
		devicePrefix, rdmaDevice))

	return deviceNodes, envs, nil
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

// Unprepare restores device bindings and removes claim artifacts using saved state.
func (s *Manager) Unprepare(claimUID string, preparedDevices drasriovtypes.PreparedDevices) error {
	var errs []error

	if err := s.cleanDeviceInfoFilesForPreparedDevicesIfNeeded(context.Background(), preparedDevices); err != nil {
		errs = append(errs, fmt.Errorf("unable to clean device-info files for claim: %v", err))
	}

	if err := s.unprepareDevices(preparedDevices); err != nil {
		errs = append(errs, fmt.Errorf("unprepare failed: %v", err))
	}

	err := s.cdi.DeleteSpecFile(claimUID)
	if err != nil {
		errs = append(errs, fmt.Errorf("unable to delete CDI spec file for claim %s: %w", claimUID, err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// unprepareDevices reverts the driver configuration for the prepared devices
func (s *Manager) unprepareDevices(preparedDevices drasriovtypes.PreparedDevices) error {
	logger := klog.FromContext(context.Background()).WithName("unprepareDevices")
	for _, preparedDevice := range preparedDevices {
		if preparedDevice == nil {
			logger.V(2).Info("Skipping nil prepared device entry during unprepare")
			continue
		}
		if preparedDevice.Config == nil {
			logger.V(2).Info("Skipping prepared device with nil config during unprepare", "device", preparedDevice.PciAddress)
			continue
		}
		// Restore original driver if a driver change was made
		if preparedDevice.Config.Driver != "" {
			if err := host.GetHelpers().RestoreDeviceDriver(preparedDevice.PciAddress, preparedDevice.OriginalDriver); err != nil {
				logger.Error(err, "Failed to restore original driver for device", "device", preparedDevice.PciAddress, "originalDriver", preparedDevice.OriginalDriver)
				return fmt.Errorf("failed to restore original driver for device %s: %w", preparedDevice.PciAddress, err)
			}
			logger.V(2).Info("Successfully restored original driver for device", "device", preparedDevice.PciAddress, "originalDriver", preparedDevice.OriginalDriver)
		}
	}
	return nil
}

// GetAdvertisedDevices returns only devices that are matched by a policy.
func (s *Manager) GetAdvertisedDevices() drasriovtypes.AllocatableDevices {
	result := make(drasriovtypes.AllocatableDevices, len(s.policyAttrKeys))
	for name := range s.policyAttrKeys {
		if device, exists := s.allocatable[name]; exists {
			result[name] = device
		}
	}
	return result
}

// UpdatePolicyDevices updates the set of advertised devices and their policy-applied attributes.
// Keys in policyDevices are device names matched by policies (these will be advertised).
// Values are additional attributes from resolved DeviceAttributes objects.
// Devices not in the map have their policy-set attributes cleared and are excluded from advertisement.
func (s *Manager) UpdatePolicyDevices(ctx context.Context, policyDevices map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) error {
	logger := klog.FromContext(ctx).WithName("UpdatePolicyDevices")
	logger.V(2).Info("Updating policy devices", "policyDeviceCount", len(policyDevices))

	changesMade := false

	// Clear policy attributes from devices no longer in the policy set
	for deviceName := range s.policyAttrKeys {
		if _, stillMatched := policyDevices[deviceName]; !stillMatched {
			if s.clearPolicyAttributes(deviceName) {
				changesMade = true
				logger.V(3).Info("Cleared policy attributes for unadvertised device", "deviceName", deviceName)
			}
		}
	}

	// Detect advertised set changes
	if !changesMade {
		if len(policyDevices) != len(s.policyAttrKeys) {
			changesMade = true
		} else {
			for name := range policyDevices {
				if _, ok := s.policyAttrKeys[name]; !ok {
					changesMade = true
					break
				}
			}
		}
	}

	// Apply policy attributes to matched devices
	for deviceName, attrs := range policyDevices {
		device, exists := s.allocatable[deviceName]
		if !exists {
			continue
		}

		if device.Attributes == nil {
			device.Attributes = make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute)
		}

		// Track new policy attribute keys for this device
		newKeys := make(map[resourceapi.QualifiedName]bool, len(attrs))
		for key, val := range attrs {
			// Protect built-in discovery attributes from override
			if consts.ReservedAttributes[key] {
				logger.Info("WARNING: skipping policy attribute that collides with a built-in discovery attribute",
					"deviceName", deviceName, "key", key)
				continue
			}
			newKeys[key] = true
			if existing, ok := device.Attributes[key]; !ok || !deviceAttributeEqual(existing, val) {
				device.Attributes[key] = val
				changesMade = true
				logger.V(3).Info("Set policy attribute", "deviceName", deviceName, "key", key)
			}
		}

		// Clear old policy attributes that are no longer in the new set
		if oldKeys, ok := s.policyAttrKeys[deviceName]; ok {
			for oldKey := range oldKeys {
				if !newKeys[oldKey] {
					delete(device.Attributes, oldKey)
					changesMade = true
					logger.V(3).Info("Cleared stale policy attribute", "deviceName", deviceName, "key", oldKey)
				}
			}
		}

		s.allocatable[deviceName] = device
		if s.policyAttrKeys == nil {
			s.policyAttrKeys = make(map[string]map[resourceapi.QualifiedName]bool)
		}
		s.policyAttrKeys[deviceName] = newKeys
	}

	if !changesMade {
		logger.V(2).Info("No changes to policy devices")
		return nil
	}

	logger.Info("Policy devices updated", "totalDevices", len(s.allocatable), "advertisedDevices", len(s.policyAttrKeys))
	if s.republishCallback != nil {
		if err := s.republishCallback(ctx); err != nil {
			logger.Error(err, "Failed to republish resources after policy update")
			return fmt.Errorf("failed to republish resources: %w", err)
		}
	}

	return nil
}

// clearPolicyAttributes removes all policy-set attributes from a device.
func (s *Manager) clearPolicyAttributes(deviceName string) bool {
	oldKeys, ok := s.policyAttrKeys[deviceName]
	if !ok || len(oldKeys) == 0 {
		delete(s.policyAttrKeys, deviceName)
		return false
	}

	device, exists := s.allocatable[deviceName]
	if !exists {
		delete(s.policyAttrKeys, deviceName)
		return false
	}

	for key := range oldKeys {
		delete(device.Attributes, key)
	}
	s.allocatable[deviceName] = device
	delete(s.policyAttrKeys, deviceName)
	return true
}

func deviceAttributeEqual(a, b resourceapi.DeviceAttribute) bool {
	return reflect.DeepEqual(a, b)
}

func cloneDeviceAttributes(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) map[string]resourceapi.DeviceAttribute {
	if len(attrs) == 0 {
		return nil
	}
	cloned := make(map[string]resourceapi.DeviceAttribute, len(attrs))
	for key, value := range attrs {
		cloned[string(key)] = value
	}
	return cloned
}

func buildMetadataAttributes(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, ifName string) map[string]resourceapi.DeviceAttribute {
	metadataAttributes := cloneDeviceAttributes(attrs)
	if ifName == "" {
		return metadataAttributes
	}

	if metadataAttributes == nil {
		metadataAttributes = make(map[string]resourceapi.DeviceAttribute, 1)
	}
	ifNameValue := ifName
	metadataAttributes[consts.AttributeInterfaceName] = resourceapi.DeviceAttribute{
		StringValue: &ifNameValue,
	}
	return metadataAttributes
}

// SetRepublishCallback sets the callback function to trigger resource republishing
func (s *Manager) SetRepublishCallback(callback func(context.Context) error) {
	s.republishCallback = callback
}
