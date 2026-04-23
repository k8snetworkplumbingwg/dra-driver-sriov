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
	PciAddress  string
	NetName     string
	VendorID    string
	DeviceID    string
	Address     string
	EswitchMode string
	PCIeRoot    string
	LinkType    string
	NumaNode    string
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
		if device == nil || device.Class == nil {
			logger.V(2).Info("Skipping nil or malformed PCI device entry")
			continue
		}
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
		// -1 indicates NUMA is not supported/enabled (standard Linux convention)
		numaNode, err := host.GetHelpers().GetNumaNode(device.Address)
		if err != nil {
			logger.Error(err, "Failed to get NUMA node, using -1 (not supported)", "address", device.Address)
			numaNode = "-1"
		}

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
			"numaNode", numaNode,
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
			NumaNode:    numaNode,
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

		// Parse NUMA node value. Keep the actual value including -1 which indicates
		// NUMA is not supported/enabled (standard Linux convention).
		// This allows users to filter devices based on NUMA availability.
		numaNodeInt, err := strconv.ParseInt(pfInfo.NumaNode, 10, 64)
		if err != nil {
			logger.Error(err, "Failed to parse NUMA node, defaulting to -1",
				"pf", pfInfo.NetName, "numaNodeStr", pfInfo.NumaNode)
			numaNodeInt = -1
		}
		numaNodeIntPtr := ptr.To(numaNodeInt)

		for _, vfInfo := range vfList {
			deviceName := pciAddressToDeviceName(vfInfo.PciAddress)
			hasBridgeMaster, bridgeErr := host.GetHelpers().HasBridgeMaster(vfInfo.PciAddress)
			if bridgeErr != nil {
				logger.Error(bridgeErr, "Failed to check bridge master for VF, device will not be excluded by this check", "address", vfInfo.PciAddress)
			}
			if hasBridgeMaster {
				logger.Info("Skipping VF because it is attached to a bridge", "address", vfInfo.PciAddress, "pfAddress", pfInfo.PciAddress)
				continue
			}
			isDefaultRoute, routeErr := host.GetHelpers().HasDefaultRoute(vfInfo.PciAddress)
			if routeErr != nil {
				logger.Error(routeErr, "Failed to check default route for VF, device will not be excluded by this check", "address", vfInfo.PciAddress)
			}
			if isDefaultRoute {
				logger.Info("Skipping VF because it has a default route", "address", vfInfo.PciAddress, "pfAddress", pfInfo.PciAddress)
				continue
			}

			// Check RDMA capability for this VF
			rdmaCapable := host.GetHelpers().VerifyRDMACapability(vfInfo.PciAddress)

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
				consts.AttributeInterfaceType: {
					StringValue: ptr.To(consts.InterfaceTypeVirtualFunction),
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
					IntValue: numaNodeIntPtr,
				},
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

// DiscoverPhysicalPciDevices performs a one-time inventory of physical PCI network
// devices. This inventory is used only when a policy explicitly requests
// pciAddresses.
func DiscoverPhysicalPciDevices() (types.AllocatableDevices, error) {
	logger := klog.LoggerWithName(klog.Background(), "DiscoverPhysicalPciDevices")
	logger.Info("Starting physical PCI inventory discovery")
	resourceList := types.AllocatableDevices{}

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

	for _, device := range devices {
		if device == nil || device.Class == nil {
			continue
		}
		devClass, err := strconv.ParseInt(device.Class.ID, 16, 64)
		if err != nil || devClass != consts.NetClass {
			continue
		}
		if host.GetHelpers().IsSriovVF(device.Address) {
			logger.V(2).Info("Skipping PCI device because it is already discovered through SR-IOV VF discovery", "address", device.Address)
			continue
		}
		hasBridgeMaster, bridgeErr := host.GetHelpers().HasBridgeMaster(device.Address)
		if bridgeErr != nil {
			logger.Error(bridgeErr, "Failed to check bridge master for PCI device, device will not be excluded by this check", "address", device.Address)
		}
		if hasBridgeMaster {
			logger.Info("Skipping PCI device because it is attached to a bridge", "address", device.Address)
			continue
		}
		isDefaultRoute, routeErr := host.GetHelpers().HasDefaultRoute(device.Address)
		if routeErr != nil {
			logger.Error(routeErr, "Failed to check default route for PCI device, device will not be excluded by this check", "address", device.Address)
		}
		if isDefaultRoute {
			logger.Info("Skipping PCI device because it has a default route", "address", device.Address)
			continue
		}

		vendorID := ""
		if device.Vendor != nil {
			vendorID = device.Vendor.ID
		}
		deviceID := ""
		if device.Product != nil {
			deviceID = device.Product.ID
		}

		inventoryDevice := buildPhysicalPCIDevice(logger, device.Address, vendorID, deviceID)
		logger.V(2).Info("Adding PCI inventory device to resource list", "deviceName", inventoryDevice.Name, "device", inventoryDevice)
		resourceList[inventoryDevice.Name] = inventoryDevice
	}

	logger.Info("Physical PCI inventory discovery completed", "totalDevices", len(resourceList))
	return resourceList, nil
}

// buildPhysicalPCIDevice builds a resource device entry for a physical PCI device.
func buildPhysicalPCIDevice(logger klog.Logger, pciAddress, vendorID, deviceID string) resourceapi.Device {
	numaNode := int64(-1)
	numaNodeStr, err := host.GetHelpers().GetNumaNode(pciAddress)
	if err != nil {
		logger.V(2).Info("Failed to get NUMA node for physical PCI inventory", "pciAddress", pciAddress, "error", err.Error())
	} else {
		parsedNumaNode, parseErr := strconv.ParseInt(numaNodeStr, 10, 64)
		if parseErr != nil {
			logger.V(2).Info("Failed to parse NUMA node for physical PCI inventory", "pciAddress", pciAddress, "numaNode", numaNodeStr, "error", parseErr.Error())
		} else {
			numaNode = parsedNumaNode
		}
	}

	pcieRoot := ""
	if resolvedPCIeRoot, rootErr := host.GetHelpers().GetPCIeRoot(pciAddress); rootErr != nil {
		logger.V(2).Info("Failed to get PCIe root for physical PCI inventory", "pciAddress", pciAddress, "error", rootErr.Error())
	} else {
		pcieRoot = resolvedPCIeRoot
	}

	linkType := consts.LinkTypeUnknown
	if resolvedLinkType, linkErr := host.GetHelpers().GetLinkType(pciAddress); linkErr != nil {
		logger.V(2).Info("Failed to get link type for physical PCI inventory", "pciAddress", pciAddress, "error", linkErr.Error())
	} else {
		linkType = resolvedLinkType
	}

	rdmaCapable := host.GetHelpers().VerifyRDMACapability(pciAddress)
	deviceName := pciAddressToDeviceName(pciAddress)
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		consts.AttributeVendorID: {
			StringValue: ptr.To(vendorID),
		},
		consts.AttributeDeviceID: {
			StringValue: ptr.To(deviceID),
		},
		consts.AttributePciAddress: {
			StringValue: ptr.To(pciAddress),
		},
		consts.AttributeMultusDeviceID: {
			StringValue: ptr.To(pciAddress),
		},
		consts.AttributeStandardPciAddress: {
			StringValue: ptr.To(pciAddress),
		},
		consts.AttributePCIeRoot: {
			StringValue: ptr.To(pcieRoot),
		},
		consts.AttributeLinkType: {
			StringValue: ptr.To(linkType),
		},
		consts.AttributeRDMACapable: {
			BoolValue: ptr.To(rdmaCapable),
		},
		consts.AttributeNUMANode: {
			IntValue: ptr.To(numaNode),
		},
		consts.AttributeInterfaceType: {
			StringValue: ptr.To(consts.InterfaceTypeRegular),
		},
	}

	return resourceapi.Device{
		Name:       deviceName,
		Attributes: attributes,
	}
}

// pciAddressToDeviceName converts a PCI BDF address into a Kubernetes-safe device name.
func pciAddressToDeviceName(pciAddress string) string {
	deviceName := strings.ReplaceAll(pciAddress, ":", "-")
	return strings.ReplaceAll(deviceName, ".", "-")
}
