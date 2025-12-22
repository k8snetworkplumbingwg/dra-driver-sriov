package devicestate

import (
	"fmt"
	"strconv"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

type PFInfo struct {
	PciAddress       string
	NetName          string
	VendorID         string
	DeviceID         string
	Address          string
	EswitchMode      string
	NumaNode         string
	PCIeRoot         string
	ParentPciAddress string
	LinkType         string
}

func DiscoverSriovDevices() (types.AllocatableDevices, error) {
	logger := klog.LoggerWithName(klog.Background(), "DiscoverSriovDevices")
	pfList := []PFInfo{}
	resourceList := types.AllocatableDevices{}

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

		pfNetName := host.GetHelpers().TryGetInterfaceName(device.Address)
		if pfNetName == "" {
			logger.Error(nil, "Unable to get interface name for device, skipping", "address", device.Address)
			continue
		}

		eswitchMode := host.GetHelpers().GetNicSriovMode(device.Address)

		// Get NUMA node information
		numaNode, err := host.GetHelpers().GetNumaNode(device.Address)
		if err != nil {
			logger.Error(err, "Failed to get NUMA node, using default", "address", device.Address)
			numaNode = "0" // Default to node 0 if we can't determine it
		}

		// Get PCIe Root Complex information using upstream Kubernetes implementation
		pcieRoot, err := host.GetHelpers().GetPCIeRoot(device.Address)
		if err != nil {
			logger.Error(err, "Failed to get PCIe Root Complex", "address", device.Address)
			pcieRoot = "" // Leave empty if we can't determine it
		}

		// Get immediate parent PCI address (e.g., bridge) for granular filtering
		parentPciAddress, err := host.GetHelpers().GetParentPciAddress(device.Address)
		if err != nil {
			logger.Error(err, "Failed to get parent PCI address", "address", device.Address)
			parentPciAddress = "" // Leave empty if we can't determine it
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
			"numaNode", numaNode,
			"pcieRoot", pcieRoot,
			"parentPciAddress", parentPciAddress,
			"linkType", linkType)

		pfList = append(pfList, PFInfo{
			PciAddress:       device.Address,
			NetName:          pfNetName,
			VendorID:         device.Vendor.ID,
			DeviceID:         device.Product.ID,
			Address:          device.Address,
			EswitchMode:      eswitchMode,
			NumaNode:         numaNode,
			PCIeRoot:         pcieRoot,
			ParentPciAddress: parentPciAddress,
			LinkType:         linkType,
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

			logger.V(2).Info("Adding VF device to resource list",
				"deviceName", deviceName,
				"vfAddress", vfInfo.PciAddress,
				"vfID", vfInfo.VFID,
				"vfDeviceID", vfInfo.DeviceID,
				"pfDeviceID", pfInfo.DeviceID,
				"pf", pfInfo.NetName)

			resourceList[deviceName] = resourceapi.Device{
				Name: deviceName,
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
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
					consts.AttributePFName: {
						StringValue: ptr.To(pfInfo.NetName),
					},
					consts.AttributeEswitchMode: {
						StringValue: ptr.To(pfInfo.EswitchMode),
					},
					consts.AttributeVFID: {
						IntValue: ptr.To(int64(vfInfo.VFID)),
					},
					consts.AttributeNumaNode: {
						IntValue: func() *int64 {
							numaNodeInt, err := strconv.ParseInt(pfInfo.NumaNode, 10, 64)
							if err != nil {
								// Default to -1 if parsing fails
								return ptr.To(int64(-1))
							}
							return ptr.To(numaNodeInt)
						}(),
					},
					// PCIe Root Complex (upstream Kubernetes standard) - for topology-aware scheduling
					consts.AttributePCIeRoot: {
						StringValue: ptr.To(pfInfo.PCIeRoot),
					},
					// Immediate parent PCI address - for granular filtering
					consts.AttributeParentPciAddress: {
						StringValue: ptr.To(pfInfo.ParentPciAddress),
					},
					// Standard Kubernetes PCI address attribute
					consts.AttributeStandardPciAddress: {
						StringValue: ptr.To(vfInfo.PciAddress),
					},
					// Link type (ethernet, infiniband, etc.)
					consts.AttributeLinkType: {
						StringValue: ptr.To(pfInfo.LinkType),
					},
				},
			}
		}
	}

	logger.Info("SR-IOV device discovery completed", "totalDevices", len(resourceList))
	return resourceList, nil
}
