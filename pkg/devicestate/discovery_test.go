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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(false)
			mockHost.EXPECT().VerifyRDMACapability("0000:01:00.2").Return(false)
			// First VF exposes a vhost vDPA device, second exposes none.
			mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return(consts.VdpaTypeVhost)
			mockHost.EXPECT().GetVdpaType("0000:01:00.2").Return("")
			// Ethernet PF: GetIBPKey is not called.

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
			Expect(dev1.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To(consts.EswitchModeLegacy)))
			Expect(dev1.Attributes[consts.AttributeVFID].IntValue).To(Equal(ptr.To(int64(0))))
			Expect(dev1.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
			Expect(dev1.Attributes[consts.AttributePfPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.0")))
			Expect(dev1.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
			Expect(dev1.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeEthernet)))
			// Compatibility attributes
			Expect(dev1.Attributes[consts.AttributeNUMANode].IntValue).To(Equal(ptr.To(int64(0))))
			// vDPA type published for the first VF; no pKey on an ethernet PF.
			Expect(dev1.Attributes[consts.AttributeVdpaType].StringValue).To(Equal(ptr.To(consts.VdpaTypeVhost)))
			_, hasPKey := dev1.Attributes[consts.AttributePKey]
			Expect(hasPKey).To(BeFalse())

			// Check second VF
			dev2 := devices["0000-01-00-2"]
			Expect(dev2.Name).To(Equal("0000-01-00-2"))
			Expect(dev2.Attributes[consts.AttributeVFID].IntValue).To(Equal(ptr.To(int64(1))))
			Expect(dev2.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.2")))
			// No vDPA device -> attribute omitted entirely.
			_, hasVdpa := dev2.Attributes[consts.AttributeVdpaType]
			Expect(hasVdpa).To(BeFalse())
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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)

			// Second PF
			mockHost.EXPECT().IsSriovVF("0000:02:00.0").Return(false)
			mockHost.EXPECT().TryGetPFInterfaceName("0000:02:00.0").Return("eth1")
			mockHost.EXPECT().GetNicSriovMode("0000:02:00.0").Return(consts.EswitchModeSwitchdev)
			mockHost.EXPECT().GetNumaNode("0000:02:00.0").Return("1", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:02:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:02:00.0").Return(consts.LinkTypeInfiniband, nil)

			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList1, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(false)
			mockHost.EXPECT().GetVFList("0000:02:00.0").Return(vfList2, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:02:00.1").Return(false)
			// Neither VF exposes a vDPA device.
			mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return("")
			mockHost.EXPECT().GetVdpaType("0000:02:00.1").Return("")
			// Only the InfiniBand PF's VF is queried for a pKey.
			mockHost.EXPECT().GetIBPKey("0000:02:00.1").Return("0x7fff")
			// Only the switchdev PF's VF is queried for a representor.
			mockHost.EXPECT().GetVFRepresentor("0000:02:00.0", 0).Return("eth1_0", "pf0vf0", nil)

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(2))

			// Check Intel VF
			dev1 := devices["0000-01-00-1"]
			Expect(dev1.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("8086")))
			Expect(dev1.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("eth0")))
			Expect(dev1.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To(consts.EswitchModeLegacy)))
			Expect(dev1.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
			Expect(dev1.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
			Expect(dev1.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeEthernet)))
			// Compatibility attributes
			Expect(dev1.Attributes[consts.AttributeNUMANode].IntValue).To(Equal(ptr.To(int64(0))))
			// Ethernet PF, no vDPA: neither pKey nor vdpaType are published.
			_, hasPKey := dev1.Attributes[consts.AttributePKey]
			Expect(hasPKey).To(BeFalse())
			_, hasVdpa := dev1.Attributes[consts.AttributeVdpaType]
			Expect(hasVdpa).To(BeFalse())
			// Legacy PF: no representor attributes.
			_, hasRep := dev1.Attributes[consts.AttributeRepresentor]
			Expect(hasRep).To(BeFalse())
			_, hasPPN := dev1.Attributes[consts.AttributePhysPortName]
			Expect(hasPPN).To(BeFalse())

			// Check Mellanox VF
			dev2 := devices["0000-02-00-1"]
			Expect(dev2.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("15b3")))
			Expect(dev2.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("eth1")))
			Expect(dev2.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To(consts.EswitchModeSwitchdev)))
			Expect(dev2.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
			Expect(dev2.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:02:00.1")))
			Expect(dev2.Attributes[consts.AttributeLinkType].StringValue).To(Equal(ptr.To(consts.LinkTypeInfiniband)))
			// Compatibility attributes
			Expect(dev2.Attributes[consts.AttributeNUMANode].IntValue).To(Equal(ptr.To(int64(1))))
			// InfiniBand PF: pKey published, still no vDPA.
			Expect(dev2.Attributes[consts.AttributePKey].StringValue).To(Equal(ptr.To("0x7fff")))
			_, hasVdpa2 := dev2.Attributes[consts.AttributeVdpaType]
			Expect(hasVdpa2).To(BeFalse())
			// Switchdev PF: representor and phys_port_name published.
			Expect(dev2.Attributes[consts.AttributeRepresentor].StringValue).To(Equal(ptr.To("eth1_0")))
			Expect(dev2.Attributes[consts.AttributePhysPortName].StringValue).To(Equal(ptr.To("pf0vf0")))
		})

		It("should set PF PCI address on VF devices", func() {
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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(false)
			mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return("")

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))

			dev := devices["0000-01-00-1"]
			Expect(dev.Attributes[consts.AttributePfPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.0")))
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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return("", fmt.Errorf("lookup failed"))
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(false)
			mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return("")

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

		Context("RDMA Capability", func() {
			var (
				pciInfo *pci.Info
				vfList  []host.VFInfo
			)

			BeforeEach(func() {
				pciInfo = &pci.Info{
					Devices: []*pci.Device{
						{
							Address: "0000:01:00.0",
							Class:   &pcidb.Class{ID: "02"},
							Vendor:  &pcidb.Vendor{ID: "15b3"},  // Mellanox
							Product: &pcidb.Product{ID: "1017"}, // ConnectX-5
						},
					},
				}

				vfList = []host.VFInfo{
					{
						PciAddress: "0000:01:00.1",
						VFID:       0,
						DeviceID:   "1018",
					},
				}

				mockHost.EXPECT().PCI().Return(pciInfo, nil)
				mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false)
				mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("ib0")
				mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeSwitchdev)
				mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("1", nil)
				mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
				mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeInfiniband, nil)
			})

			It("should discover RDMA-capable VFs with RDMA attributes", func() {
				// Add a second VF to the list for this specific test
				localVfList := []host.VFInfo{
					{
						PciAddress: "0000:01:00.1",
						VFID:       0,
						DeviceID:   "1018",
					},
					{
						PciAddress: "0000:01:00.2",
						VFID:       1,
						DeviceID:   "1018",
					},
				}
				mockHost.EXPECT().GetVFList("0000:01:00.0").Return(localVfList, nil)

				// First VF is RDMA-capable
				mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(true)

				// Second VF is not RDMA-capable
				mockHost.EXPECT().VerifyRDMACapability("0000:01:00.2").Return(false)

				// First VF exposes a virtio vDPA device; second exposes none.
				mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return(consts.VdpaTypeVirtio)
				mockHost.EXPECT().GetVdpaType("0000:01:00.2").Return("")
				// InfiniBand PF: both VFs are queried for a pKey.
				mockHost.EXPECT().GetIBPKey("0000:01:00.1").Return("0x8001")
				mockHost.EXPECT().GetIBPKey("0000:01:00.2").Return("")
				// Switchdev PF: first VF resolves a representor, second fails the
				// lookup (representor attributes must then be omitted).
				mockHost.EXPECT().GetVFRepresentor("0000:01:00.0", 0).Return("ib0_0", "pf0vf0", nil)
				mockHost.EXPECT().GetVFRepresentor("0000:01:00.0", 1).Return("", "", fmt.Errorf("representor not found"))

				devices, err := DiscoverSriovDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(HaveLen(2))

				// Check first VF (RDMA-capable)
				dev1 := devices["0000-01-00-1"]
				Expect(dev1.Name).To(Equal("0000-01-00-1"))
				Expect(dev1.Attributes[consts.AttributeVendorID].StringValue).To(Equal(ptr.To("15b3")))
				Expect(dev1.Attributes[consts.AttributeDeviceID].StringValue).To(Equal(ptr.To("1018")))
				Expect(dev1.Attributes[consts.AttributePFDeviceID].StringValue).To(Equal(ptr.To("1017")))
				Expect(dev1.Attributes[consts.AttributePciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
				Expect(dev1.Attributes[consts.AttributePFName].StringValue).To(Equal(ptr.To("ib0")))
				Expect(dev1.Attributes[consts.AttributeEswitchMode].StringValue).To(Equal(ptr.To(consts.EswitchModeSwitchdev)))
				Expect(dev1.Attributes[consts.AttributeVFID].IntValue).To(Equal(ptr.To(int64(0))))
				Expect(dev1.Attributes[consts.AttributePCIeRoot].StringValue).To(Equal(ptr.To("pci0000:00")))
				Expect(dev1.Attributes[consts.AttributePfPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.0")))
				Expect(dev1.Attributes[consts.AttributeStandardPciAddress].StringValue).To(Equal(ptr.To("0000:01:00.1")))
				// RDMA-specific attributes
				Expect(dev1.Attributes[consts.AttributeRDMACapable].BoolValue).To(Equal(ptr.To(true)))
				// Compatibility attributes
				Expect(dev1.Attributes[consts.AttributeNUMANode].IntValue).To(Equal(ptr.To(int64(1))))
				// Selector-parity attributes: vDPA type and IB pKey both published.
				Expect(dev1.Attributes[consts.AttributeVdpaType].StringValue).To(Equal(ptr.To(consts.VdpaTypeVirtio)))
				Expect(dev1.Attributes[consts.AttributePKey].StringValue).To(Equal(ptr.To("0x8001")))
				// Switchdev PF: representor and phys_port_name published.
				Expect(dev1.Attributes[consts.AttributeRepresentor].StringValue).To(Equal(ptr.To("ib0_0")))
				Expect(dev1.Attributes[consts.AttributePhysPortName].StringValue).To(Equal(ptr.To("pf0vf0")))

				// Check second VF (not RDMA-capable)
				dev2 := devices["0000-01-00-2"]
				Expect(dev2.Name).To(Equal("0000-01-00-2"))
				Expect(dev2.Attributes[consts.AttributeVFID].IntValue).To(Equal(ptr.To(int64(1))))
				Expect(dev2.Attributes[consts.AttributeRDMACapable].BoolValue).To(Equal(ptr.To(false)))
				// No vDPA device and an empty pKey -> both attributes omitted.
				_, hasVdpa := dev2.Attributes[consts.AttributeVdpaType]
				Expect(hasVdpa).To(BeFalse())
				_, hasPKey := dev2.Attributes[consts.AttributePKey]
				Expect(hasPKey).To(BeFalse())
				// Representor lookup failed -> representor attributes omitted.
				_, hasRep := dev2.Attributes[consts.AttributeRepresentor]
				Expect(hasRep).To(BeFalse())
				_, hasPPN := dev2.Attributes[consts.AttributePhysPortName]
				Expect(hasPPN).To(BeFalse())
			})

			It("should handle RDMA capability check errors gracefully", func() {
				mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
				// RDMA capability check fails (returns false)
				mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(false)
				mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return("")
				mockHost.EXPECT().GetIBPKey("0000:01:00.1").Return("")
				mockHost.EXPECT().GetVFRepresentor("0000:01:00.0", 0).Return("ib0_0", "pf0vf0", nil)

				devices, err := DiscoverSriovDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(HaveLen(1))

				// Should default to not RDMA capable
				dev := devices["0000-01-00-1"]
				Expect(dev.Attributes[consts.AttributeRDMACapable].BoolValue).To(Equal(ptr.To(false)))
			})
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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:01:00.1").Return(false)
			mockHost.EXPECT().GetVdpaType("0000:01:00.1").Return("")

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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("") // No interface name

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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:af:10.7").Return(false)
			mockHost.EXPECT().GetVdpaType("0000:af:10.7").Return("")

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
			mockHost.EXPECT().TryGetPFInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return(consts.EswitchModeLegacy)
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return([]host.VFInfo{}, nil) // Empty list

			devices, err := DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
		})
	})
})
