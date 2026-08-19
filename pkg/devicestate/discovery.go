package devicestate

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

type PFInfo struct {
	PciAddress  string
	NetName     string
	VendorID    string
	DeviceID    string
	Address     string
	EswitchMode string
	PCIeRoot    string
	LinkType    string
}

type discoveryOptions struct {
	numaAttributeForm deviceattribute.AttributeForm
}

// DiscoveryOption configures SR-IOV device discovery.
type DiscoveryOption func(*discoveryOptions)

// WithListNUMAAttributes publishes resource.kubernetes.io/numaNode as an
// integer list. The Kubernetes DRAListTypeAttributes feature gate must be
// enabled on the kube-apiserver and kube-scheduler before this option is used.
func WithListNUMAAttributes() DiscoveryOption {
	return func(options *discoveryOptions) {
		options.numaAttributeForm = deviceattribute.ListAttribute
	}
}

func DiscoverSriovDevices(options ...DiscoveryOption) (types.AllocatableDevices, error) {
	logger := klog.LoggerWithName(klog.Background(), "DiscoverSriovDevices")
	pfList := []PFInfo{}
	resourceList := types.AllocatableDevices{}
	discoveryConfig := &discoveryOptions{
		numaAttributeForm: deviceattribute.ScalarAttribute,
	}
	for _, option := range options {
		option(discoveryConfig)
	}

	logger.Info("Starting SR-IOV device discovery")

	pci, err := host.GetHelpers().PCI()
	if err != nil {
		logger.Error(err, "Failed to get PCI info")
		return nil, fmt.Errorf("error getting PCI info: %v", err)
	}

	devices := pci.Devices
	if len(devices) == 0 {
		logger.Info("No PCI devices found")
		return nil, fmt.Errorf("could not retrieve PCI devices")
	}

	logger.Info("Found PCI devices", "count", len(devices))

	for _, device := range devices {
		logger.V(2).Info("Processing PCI device", "address", device.Address, "class", device.Class.ID)

		devClass, err := strconv.ParseInt(device.Class.ID, 16, 64)
		if err != nil {
			logger.Error(err, "Unable to parse device class, skipping device",
				"address", device.Address, "class", device.Class.ID)
			continue
		}
		if devClass != consts.NetClass {
			logger.V(3).Info("Skipping non-network device", "address", device.Address, "class", devClass)
			continue
		}

		// TODO: exclude devices used by host system
		if host.GetHelpers().IsSriovVF(device.Address) {
			logger.V(2).Info("Skipping VF device", "address", device.Address)
			continue
		}

		pfNetName := host.GetHelpers().TryGetPFInterfaceName(device.Address)
		if pfNetName == "" {
			logger.Error(nil, "Unable to get interface name for device, skipping", "address", device.Address)
			continue
		}

		eswitchMode := host.GetHelpers().GetNicSriovMode(device.Address)

		// Get PCIe Root Complex information using upstream Kubernetes implementation
		pcieRoot, err := host.GetHelpers().GetPCIeRoot(device.Address)
		if err != nil {
			logger.Error(err, "Failed to get PCIe Root Complex", "address", device.Address)
			pcieRoot = "" // Leave empty if we can't determine it
		}

		// Get link type (ethernet, infiniband, etc.)
		linkType, err := host.GetHelpers().GetLinkType(device.Address)
		if err != nil {
			logger.Error(err, "Failed to get link type", "address", device.Address)
			linkType = consts.LinkTypeUnknown // Default to unknown if we can't determine it
		}

		logger.Info("Found SR-IOV PF device",
			"address", device.Address,
			"interface", pfNetName,
			"vendor", device.Vendor.ID,
			"device", device.Product.ID,
			"eswitchMode", eswitchMode,
			"pcieRoot", pcieRoot,
			"linkType", linkType)

		pfList = append(pfList, PFInfo{
			PciAddress:  device.Address,
			NetName:     pfNetName,
			VendorID:    device.Vendor.ID,
			DeviceID:    device.Product.ID,
			Address:     device.Address,
			EswitchMode: eswitchMode,
			PCIeRoot:    pcieRoot,
			LinkType:    linkType,
		})
	}

	logger.Info("Processing SR-IOV PF devices", "pfCount", len(pfList))

	for _, pfInfo := range pfList {
		logger.V(1).Info("Getting VF list for PF", "pf", pfInfo.NetName, "address", pfInfo.Address)

		vfList, err := host.GetHelpers().GetVFList(pfInfo.Address)
		if err != nil {
			logger.Error(err, "Failed to get VF list for PF", "pf", pfInfo.NetName, "address", pfInfo.Address)
			return nil, fmt.Errorf("error getting VF list: %v", err)
		}

		logger.Info("Found VFs for PF", "pf", pfInfo.NetName, "vfCount", len(vfList))

		for _, vfInfo := range vfList {
			deviceName := strings.ReplaceAll(vfInfo.PciAddress, ":", "-")
			deviceName = strings.ReplaceAll(deviceName, ".", "-")

			// Check RDMA capability for this VF
			rdmaCapable := host.GetHelpers().VerifyRDMACapability(vfInfo.PciAddress)

			// Derive standardized NUMA topology from the advertised VF itself. The
			// first list element is always the physical NUMA node, so it also acts
			// as the source for the scalar dra.net compatibility attribute.
			numaNodeInt := int64(-1)
			numaAttribute, numaErr := host.GetHelpers().GetNUMANodeAttribute(vfInfo.PciAddress, discoveryConfig.numaAttributeForm)
			if numaErr != nil {
				if isNUMATopologyReadFailure(numaErr) {
					logger.Error(numaErr, "Failed to read NUMA topology", "address", vfInfo.PciAddress)
				} else {
					logger.V(2).Info("Device has no NUMA affinity", "address", vfInfo.PciAddress, "error", numaErr)
				}
			} else if physicalNode, ok := physicalNUMANode(numaAttribute.Value); ok {
				numaNodeInt = physicalNode
			} else {
				numaErr = fmt.Errorf("NUMA attribute has neither a scalar nor a list value")
				logger.Error(numaErr, "Upstream NUMA helper returned an attribute without a physical node", "address", vfInfo.PciAddress)
			}

			logger.V(2).Info("Adding VF device to resource list",
				"deviceName", deviceName,
				"vfAddress", vfInfo.PciAddress,
				"vfID", vfInfo.VFID,
				"vfDeviceID", vfInfo.DeviceID,
				"pfDeviceID", pfInfo.DeviceID,
				"pf", pfInfo.NetName,
				"rdmaCapable", rdmaCapable)

			// Build device attributes
			attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				consts.AttributeVendorID: {
					StringValue: ptr.To(pfInfo.VendorID),
				},
				consts.AttributeDeviceID: {
					StringValue: ptr.To(vfInfo.DeviceID),
				},
				consts.AttributePFDeviceID: {
					StringValue: ptr.To(pfInfo.DeviceID),
				},
				consts.AttributePciAddress: {
					StringValue: ptr.To(vfInfo.PciAddress),
				},
				consts.AttributeMultusDeviceID: {
					StringValue: ptr.To(vfInfo.PciAddress),
				},
				consts.AttributePFName: {
					StringValue: ptr.To(pfInfo.NetName),
				},
				consts.AttributeEswitchMode: {
					StringValue: ptr.To(pfInfo.EswitchMode),
				},
				consts.AttributeVFID: {
					IntValue: ptr.To(int64(vfInfo.VFID)),
				},
				// PCIe Root Complex (upstream Kubernetes standard) - for topology-aware scheduling
				consts.AttributePCIeRoot: {
					StringValue: ptr.To(pfInfo.PCIeRoot),
				},
				consts.AttributePfPciAddress: {
					StringValue: ptr.To(pfInfo.PciAddress),
				},
				// Standard Kubernetes PCI address attribute
				consts.AttributeStandardPciAddress: {
					StringValue: ptr.To(vfInfo.PciAddress),
				},
				// Link type (ethernet, infiniband, etc.)
				consts.AttributeLinkType: {
					StringValue: ptr.To(pfInfo.LinkType),
				},
				consts.AttributeRDMACapable: {
					BoolValue: ptr.To(rdmaCapable),
				},
				// compatibility attributes
				consts.AttributeNUMANode: {
					IntValue: ptr.To(numaNodeInt),
				},
			}
			if numaErr == nil {
				attributes[numaAttribute.Name] = numaAttribute.Value
			}

			resourceList[deviceName] = resourceapi.Device{
				Name:       deviceName,
				Attributes: attributes,
			}
		}
	}

	logger.Info("SR-IOV device discovery completed", "totalDevices", len(resourceList))
	return resourceList, nil
}

func isNUMATopologyReadFailure(err error) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr)
}

// physicalNUMANode recovers the physical NUMA node from either representation
// defined by the standard attribute. In list form the physical node is first;
// the remaining entries are an unordered set used for topology matching.
func physicalNUMANode(attribute resourceapi.DeviceAttribute) (int64, bool) {
	if attribute.IntValue != nil {
		return *attribute.IntValue, true
	}
	if len(attribute.IntValues) > 0 {
		return attribute.IntValues[0], true
	}
	return 0, false
}
