package devicestate

import (
	"fmt"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	"k8s.io/utils/ptr"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
)

var _ = Describe("DiscoverSriovDevices", func() {
	var (
		mockCtrl    *gomock.Controller
		mockHost    *mock_host.MockInterface
		origHelpers host.Interface
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockHost = mock_host.NewMockInterface(mockCtrl)
		// Save original helpers and replace with mock
		// Force initialization first so the sync.Once is triggered
		_ = host.GetHelpers()
		origHelpers = host.Helpers
		host.Helpers = mockHost
	})

	AfterEach(func() {
		// Restore original helpers
		host.Helpers = origHelpers
		mockCtrl.Finish()
	})

	Context("Success Cases", func() {
		It("should discover single PF with VFs", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class: &pcidb.Class{
							ID: "02", // Network class
						},
						Vendor: &pcidb.Vendor{
							ID: "8086", // Intel
						},
						Product: &pcidb.Product{
							ID: "1572", // X710
						},
					},
				},
			}

			vfList := []host.VFInfo{
				{
					PciAddress: "0000:01:00.1",
					VFID:       0,
					DeviceID:   "154c",
				},
				{
					PciAddress: "0000:01:00.2",
					VFID:       1,
					DeviceID:   "154c",
				},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("0000:00:01.0", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(2))

			// Check first VF
			dev1 := devices["0000-01-00-1"]
			Expect(dev1.Name).To(Equal("0000-01-00-1"))
			Expect(dev1.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("8086")))
			Expect(dev1.Attributes[consts.AttributeDeviceID].StringValue).To(Equal(ptr.To("154c")))
			Expect(dev1.Attributes[consts.AttributePFDeviceID].StringValue).To(Equal(ptr.To("1572")))
			Expect(dev1.Attributes[consts.AttributePciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
			Expect(dev1.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("eth0")))
			Expect(dev1.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To("legacy")))
			Expect(dev1.Attributes[consts.AttributeVFID].IntValue).To(Equal(ptr.To(int64(0))))
			Expect(dev1.Attributes[consts.AttributeNumaNode].IntValue).To(Equal(ptr.To(int64(0))))
			Expect(dev1.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
			Expect(dev1.Attributes[consts.AttributeParentPciAddress].StringValue).To(Equal(ptr.To("0000:00:01.0")))
			Expect(dev1.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
			Expect(dev1.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeEthernet)))

			// Check second VF
			dev2 := devices["0000-01-00-2"]
			Expect(dev2.Name).To(Equal("0000-01-00-2"))
			Expect(dev2.Attributes[consts.AttributeVFID].IntValue).To(Equal(ptr.To(int64(1))))
			Expect(dev2.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.2")))
		})

		It("should discover multiple PFs with VFs", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
					{
						Address: "0000:02:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "15b3"},
						Product: &pcidb.Product{ID: "1017"},
					},
				},
			}

			vfList1 := []host.VFInfo{
				{PciAddress: "0000:01:00.1", VFID: 0, DeviceID: "154c"},
			}
			vfList2 := []host.VFInfo{
				{PciAddress: "0000:02:00.1", VFID: 0, DeviceID: "1018"},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)

			// First PF
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("0000:00:01.0", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)

			// Second PF
			mockHost.EXPECT().IsSriovVF("0000:02:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:02:00.0").Return("eth1")
			mockHost.EXPECT().GetNicSriovMode("0000:02:00.0").Return("switchdev")
			mockHost.EXPECT().GetNumaNode("0000:02:00.0").Return("1", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:02:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:02:00.0").Return("0000:00:02.0", nil)
			mockHost.EXPECT().GetLinkType("0000:02:00.0").Return(consts.LinkTypeInfiniband, nil)

			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList1, nil)
			mockHost.EXPECT().GetVFList("0000:02:00.0").Return(vfList2, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(2))

			// Check Intel VF
			dev1 := devices["0000-01-00-1"]
			Expect(dev1.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("8086")))
			Expect(dev1.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("eth0")))
			Expect(dev1.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To("legacy")))
			Expect(dev1.Attributes[consts.AttributeNumaNode].IntValue).To(Equal(ptr.To(int64(0))))
			Expect(dev1.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
			Expect(dev1.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
			Expect(dev1.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeEthernet)))

			// Check Mellanox VF
			dev2 := devices["0000-02-00-1"]
			Expect(dev2.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("15b3")))
			Expect(dev2.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("eth1")))
			Expect(dev2.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To("switchdev")))
			Expect(dev2.Attributes[consts.AttributeNumaNode].IntValue).To(Equal(ptr.To(int64(1))))
			Expect(dev2.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
			Expect(dev2.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:02:00.1")))
			Expect(dev2.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeInfiniband)))
		})

		It("should handle NUMA node detection failure with default", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			vfList := []host.VFInfo{
				{PciAddress: "0000:01:00.1", VFID: 0, DeviceID: "154c"},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("", fmt.Errorf("numa node not found"))
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))

			// NUMA should default to 0
			dev := devices["0000-01-00-1"]
			Expect(dev.Attributes[consts.AttributeNumaNode].IntValue).To(Equal(ptr.To(int64(0))))
			// Standard PCI address should still be set
			Expect(dev.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
		})

		It("should handle parent PCI address detection failure gracefully", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			vfList := []host.VFInfo{
				{PciAddress: "0000:01:00.1", VFID: 0, DeviceID: "154c"},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("", fmt.Errorf("parent not found"))
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))

			// Parent PCI address should be empty
			dev := devices["0000-01-00-1"]
			Expect(dev.Attributes[consts.AttributeParentPciAddress].StringValue).To(Equal(ptr.To("")))
			// Standard PCI address should still be set
			Expect(dev.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
		})

		It("should handle link type detection failure with unknown", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			vfList := []host.VFInfo{
				{PciAddress: "0000:01:00.1", VFID: 0, DeviceID: "154c"},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("0000:00:01.0", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return("", fmt.Errorf("lookup failed"))
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))

			// Link type should default to "unknown"
			dev := devices["0000-01-00-1"]
			Expect(dev.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeUnknown)))
			// Other attributes should still be set correctly
			Expect(dev.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("8086")))
			Expect(dev.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("eth0")))
			Expect(dev.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
		})
	})

	Context("Filtering Cases", func() {
		It("should skip non-network devices", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "03"}, // Display controller
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1234"},
					},
					{
						Address: "0000:02:00.0",
						Class:   &pcidb.Class{ID: "01"}, // Storage controller
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "5678"},
					},
				},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			// No other calls expected since devices are not network class

			devices, err := DiscoverSriovDevices()
			// When all devices are filtered, function returns successfully with empty list
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
		})

		It("should skip VF devices (only process PFs)", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
					{
						Address: "0000:01:00.1", // This is a VF
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "154c"},
					},
				},
			}

			vfList := []host.VFInfo{
				{PciAddress: "0000:01:00.1", VFID: 0, DeviceID: "154c"},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)

			// First device (PF)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)

			// Second device (VF) - should be skipped
			mockHost.EXPECT().IsSriovVF("0000:01:00.1").Return(true)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1)) // Only the VF from the PF's list, not the PCI device itself
		})

		It("should skip devices without interface name", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("") // No interface name

			devices, err := DiscoverSriovDevices()
			// Device is skipped, returns successfully with empty list
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
		})

		It("should skip devices with invalid class ID", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "invalid"}, // Invalid hex
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			// No other calls since parsing fails

			devices, err := DiscoverSriovDevices()
			// Device parsing fails, returns successfully with empty list
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
		})
	})

	Context("Error Cases", func() {
		It("should return error when PCI() fails", func() {
			mockHost.EXPECT().PCI().Return(nil, fmt.Errorf("failed to get PCI info"))

			devices, err := DiscoverSriovDevices()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error getting PCI info"))
			Expect(devices).To(BeNil())
		})

		It("should return error when no PCI devices found", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("could not retrieve PCI devices"))
			Expect(devices).To(BeNil())
		})

		It("should return error when GetVFList fails", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(nil, fmt.Errorf("failed to get VF list"))

			devices, err := DiscoverSriovDevices()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error getting VF list"))
			Expect(devices).To(BeNil())
		})
	})

	Context("Device Naming", func() {
		It("should convert PCI address to device name correctly", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			vfList := []host.VFInfo{
				{PciAddress: "0000:af:10.7", VFID: 0, DeviceID: "154c"}, // Complex address
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())

			// Colons and dots should be replaced with dashes
			_, exists := devices["0000-af-10-7"]
			Expect(exists).To(BeTrue())
		})
	})

	Context("Empty VF Lists", func() {
		It("should handle PF with no VFs", func() {
			pciInfo := &pci.Info{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			mockHost.EXPECT().PCI().Return(pciInfo, nil)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetParentPciAddress("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return([]host.VFInfo{}, nil) // Empty list

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
		})
	})
})
