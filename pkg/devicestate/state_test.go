package devicestate

import (
	"context"
	"fmt"
	"os"

	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

func newTestManagerWithK8sClient(objects ...crclient.Object) *Manager {
	scheme := runtime.NewScheme()
	_ = netattdefv1.AddToScheme(scheme)

	crClient := crfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		Build()

	return &Manager{
		k8sClient: flags.ClientSets{
			Interface: k8sfake.NewSimpleClientset(),
			Client:    crClient,
		},
	}
}

var _ = Describe("Manager", Serial, func() {
	var (
		mockCtrl    *gomock.Controller
		mockHost    *mock_host.MockInterface
		origHelpers host.Interface
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockHost = mock_host.NewMockInterface(mockCtrl)
		// Save original helpers and replace with mock
		_ = host.GetHelpers()
		origHelpers = host.Helpers
		host.Helpers = mockHost
	})

	AfterEach(func() {
		// Restore original helpers
		host.Helpers = origHelpers
		mockCtrl.Finish()
	})

	Context("GetAllocatableDevices", func() {
		It("should return allocatable devices", func() {
			devices := drasriovtypes.AllocatableDevices{
				"device1": resourceapi.Device{Name: "device1"},
				"device2": resourceapi.Device{Name: "device2"},
			}

			m := &Manager{
				allocatable: devices,
			}

			result := m.GetAllocatableDevices()
			Expect(result).To(HaveLen(2))
			Expect(result).To(HaveKey("device1"))
			Expect(result).To(HaveKey("device2"))
		})
	})

	Context("GetPolicyCandidateDevices", func() {
		It("returns only allocatable devices when PCI inventory is disabled", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{
					"devA": {Name: "devA"},
				},
				pciAddressInventory: drasriovtypes.AllocatableDevices{
					"devB": {Name: "devB"},
				},
			}

			candidates := m.GetPolicyCandidateDevices(false)
			Expect(candidates).To(HaveLen(1))
			Expect(candidates).To(HaveKey("devA"))
			Expect(candidates).ToNot(HaveKey("devB"))
		})

		It("includes PCI inventory devices when requested", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{
					"devA": {Name: "devA"},
				},
				pciAddressInventory: drasriovtypes.AllocatableDevices{
					"devB": {Name: "devB"},
				},
			}

			candidates := m.GetPolicyCandidateDevices(true)
			Expect(candidates).To(HaveLen(2))
			Expect(candidates).To(HaveKey("devA"))
			Expect(candidates).To(HaveKey("devB"))
		})
	})

	Context("NewManager", func() {
		It("does not publish interfaces with default route in allocatable or PCI inventory", func() {
			testCdiRoot := GinkgoT().TempDir()
			cdiHandler, err := cdi.NewHandler(testCdiRoot)
			Expect(err).NotTo(HaveOccurred())

			config := &drasriovtypes.Config{
				Flags: &drasriovtypes.Flags{
					ConfigurationMode:      string(consts.ConfigurationModeStandalone),
					DefaultInterfacePrefix: "vfnet",
				},
			}

			pciInfo := &ghw.PCIInfo{
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

			gomock.InOrder(
				mockHost.EXPECT().PCI().Return(pciInfo, nil),
				mockHost.EXPECT().PCI().Return(pciInfo, nil),
			)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false).Times(2)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(vfList, nil)
			mockHost.EXPECT().HasBridgeMaster("0000:01:00.1").Return(false, nil)
			mockHost.EXPECT().HasDefaultRoute("0000:01:00.1").Return(true, nil)
			mockHost.EXPECT().HasBridgeMaster("0000:01:00.0").Return(false, nil)
			mockHost.EXPECT().HasDefaultRoute("0000:01:00.0").Return(true, nil)

			manager, err := NewManager(config, cdiHandler, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.GetAllocatableDevices()).To(BeEmpty())
			Expect(manager.GetPolicyCandidateDevices(true)).To(BeEmpty())
		})

		It("does not publish bridge-attached interfaces from PCI inventory used by pciAddresses", func() {
			testCdiRoot := GinkgoT().TempDir()
			cdiHandler, err := cdi.NewHandler(testCdiRoot)
			Expect(err).NotTo(HaveOccurred())

			config := &drasriovtypes.Config{
				Flags: &drasriovtypes.Flags{
					ConfigurationMode:      string(consts.ConfigurationModeStandalone),
					DefaultInterfacePrefix: "vfnet",
				},
			}

			pciInfo := &ghw.PCIInfo{
				Devices: []*pci.Device{
					{
						Address: "0000:01:00.0",
						Class:   &pcidb.Class{ID: "02"},
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1572"},
					},
				},
			}

			gomock.InOrder(
				mockHost.EXPECT().PCI().Return(pciInfo, nil),
				mockHost.EXPECT().PCI().Return(pciInfo, nil),
			)
			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false).Times(2)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(nil, nil)
			mockHost.EXPECT().HasBridgeMaster("0000:01:00.0").Return(true, nil)

			manager, err := NewManager(config, cdiHandler, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.GetAllocatableDevices()).To(BeEmpty())
			Expect(manager.GetPolicyCandidateDevices(true)).To(BeEmpty())
		})

		It("keeps non-bridged inventory devices while skipping bridged ones", func() {
			testCdiRoot := GinkgoT().TempDir()
			cdiHandler, err := cdi.NewHandler(testCdiRoot)
			Expect(err).NotTo(HaveOccurred())

			config := &drasriovtypes.Config{
				Flags: &drasriovtypes.Flags{
					ConfigurationMode:      string(consts.ConfigurationModeStandalone),
					DefaultInterfacePrefix: "vfnet",
				},
			}

			pciInfo := &ghw.PCIInfo{
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
						Vendor:  &pcidb.Vendor{ID: "8086"},
						Product: &pcidb.Product{ID: "1889"},
					},
				},
			}

			gomock.InOrder(
				mockHost.EXPECT().PCI().Return(pciInfo, nil),
				mockHost.EXPECT().PCI().Return(pciInfo, nil),
			)

			mockHost.EXPECT().IsSriovVF("0000:01:00.0").Return(false).Times(2)
			mockHost.EXPECT().TryGetInterfaceName("0000:01:00.0").Return("eth0")
			mockHost.EXPECT().GetNicSriovMode("0000:01:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:01:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:01:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:01:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:01:00.0").Return(nil, nil)

			mockHost.EXPECT().IsSriovVF("0000:02:00.0").Return(false).Times(2)
			mockHost.EXPECT().TryGetInterfaceName("0000:02:00.0").Return("eth1")
			mockHost.EXPECT().GetNicSriovMode("0000:02:00.0").Return("legacy")
			mockHost.EXPECT().GetNumaNode("0000:02:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:02:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:02:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().GetVFList("0000:02:00.0").Return(nil, nil)

			mockHost.EXPECT().HasBridgeMaster("0000:01:00.0").Return(true, nil)
			mockHost.EXPECT().HasBridgeMaster("0000:02:00.0").Return(false, nil)
			mockHost.EXPECT().HasDefaultRoute("0000:02:00.0").Return(false, nil)
			mockHost.EXPECT().GetNumaNode("0000:02:00.0").Return("0", nil)
			mockHost.EXPECT().GetPCIeRoot("0000:02:00.0").Return("pci0000:00", nil)
			mockHost.EXPECT().GetLinkType("0000:02:00.0").Return(consts.LinkTypeEthernet, nil)
			mockHost.EXPECT().VerifyRDMACapability("0000:02:00.0").Return(false)

			manager, err := NewManager(config, cdiHandler, nil)
			Expect(err).NotTo(HaveOccurred())
			candidates := manager.GetPolicyCandidateDevices(true)
			Expect(candidates).To(HaveLen(1))
			Expect(candidates).To(HaveKey("0000-02-00-0"))
			Expect(candidates).ToNot(HaveKey("0000-01-00-0"))
		})
	})

	Context("GetAllocatableDeviceByName", func() {
		It("should return device when it exists", func() {
			devices := drasriovtypes.AllocatableDevices{
				"device1": resourceapi.Device{Name: "device1"},
			}

			m := &Manager{
				allocatable: devices,
			}

			device, exists := m.GetAllocatableDeviceByName("device1")
			Expect(exists).To(BeTrue())
			Expect(device.Name).To(Equal("device1"))
		})

		It("should return false when device does not exist", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{},
			}

			_, exists := m.GetAllocatableDeviceByName("nonexistent")
			Expect(exists).To(BeFalse())
		})

		It("should return device from PCI inventory when missing from allocatable", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{},
				pciAddressInventory: drasriovtypes.AllocatableDevices{
					"dev-from-inventory": {Name: "dev-from-inventory"},
				},
			}

			device, exists := m.GetAllocatableDeviceByName("dev-from-inventory")
			Expect(exists).To(BeTrue())
			Expect(device.Name).To(Equal("dev-from-inventory"))
		})
	})

	Context("normalizeConfigurationMode", func() {
		It("defaults empty mode to STANDALONE", func() {
			mode, err := normalizeConfigurationMode("")
			Expect(err).NotTo(HaveOccurred())
			Expect(mode).To(Equal(string(consts.ConfigurationModeStandalone)))
		})

		It("rejects unsupported modes", func() {
			_, err := normalizeConfigurationMode("UNKNOWN")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unsupported configuration mode"))
		})

		It("accepts explicit STANDALONE mode", func() {
			mode, err := normalizeConfigurationMode(string(consts.ConfigurationModeStandalone))
			Expect(err).NotTo(HaveOccurred())
			Expect(mode).To(Equal(string(consts.ConfigurationModeStandalone)))
		})

		It("accepts explicit MULTUS mode", func() {
			mode, err := normalizeConfigurationMode(string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			Expect(mode).To(Equal(string(consts.ConfigurationModeMultus)))
		})
	})

	Context("getNetAttachDefRawConfig", func() {
		It("should return network attachment definition config", func() {
			netAttachDef := &netattdefv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-net",
					Namespace: "test-ns",
				},
				Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
					Config: `{"cniVersion":"0.3.1","type":"sriov"}`,
				},
			}

			m := newTestManagerWithK8sClient(netAttachDef)

			config, err := m.getNetAttachDefRawConfig(context.Background(), "test-ns", "test-net")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).To(Equal(`{"cniVersion":"0.3.1","type":"sriov"}`))
		})

		It("should return error when network attachment definition does not exist", func() {
			m := newTestManagerWithK8sClient()

			_, err := m.getNetAttachDefRawConfig(context.Background(), "test-ns", "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("unprepareDevices", func() {
		It("should restore original driver when driver was changed", func() {
			preparedDevices := drasriovtypes.PreparedDevices{
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "ixgbevf",
					Config: &configapi.VfConfig{
						Driver: "vfio-pci",
					},
				},
			}

			mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.1", "ixgbevf").Return(nil)

			m := &Manager{}
			err := m.unprepareDevices(preparedDevices)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return error when restore fails", func() {
			preparedDevices := drasriovtypes.PreparedDevices{
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "ixgbevf",
					Config: &configapi.VfConfig{
						Driver: "vfio-pci",
					},
				},
			}

			mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.1", "ixgbevf").
				Return(fmt.Errorf("restore failed"))

			m := &Manager{}
			err := m.unprepareDevices(preparedDevices)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to restore original driver"))
		})

		It("continues restoring remaining devices when one restore fails", func() {
			preparedDevices := drasriovtypes.PreparedDevices{
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "ixgbevf",
					Config: &configapi.VfConfig{
						Driver: consts.VFIODriverName,
					},
				},
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.2",
					OriginalDriver: "mlx5_core",
					Config: &configapi.VfConfig{
						Driver: consts.VFIODriverName,
					},
				},
			}

			mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.1", "ixgbevf").Return(fmt.Errorf("restore failed"))
			mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.2", "mlx5_core").Return(nil)

			m := &Manager{}
			err := m.unprepareDevices(preparedDevices)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to restore original driver"))
		})

		It("should skip driver restoration when no driver was set", func() {
			preparedDevices := drasriovtypes.PreparedDevices{
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "ixgbevf",
					Config:         &configapi.VfConfig{},
				},
			}

			// No mock expectation - RestoreDeviceDriver should not be called

			m := &Manager{}
			err := m.unprepareDevices(preparedDevices)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should skip nil and nil-config prepared device entries", func() {
			preparedDevices := drasriovtypes.PreparedDevices{
				nil,
				&drasriovtypes.PreparedDevice{
					PciAddress: "0000:01:00.2",
				},
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.3",
					OriginalDriver: "ixgbevf",
					Config:         &configapi.VfConfig{},
				},
			}

			m := &Manager{}
			err := m.unprepareDevices(preparedDevices)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Unprepare", func() {
		It("should call unprepareDevices and attempt to delete CDI spec files", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			preparedDevices := drasriovtypes.PreparedDevices{
				&drasriovtypes.PreparedDevice{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "",
					PodUID:         "pod-uid-123",
					Config:         &configapi.VfConfig{},
				},
			}

			m := &Manager{
				cdi: cdiHandler,
			}

			err = m.Unprepare("claim-uid-123", preparedDevices)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not panic when preparedDevices is empty", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := &Manager{
				cdi: cdiHandler,
			}

			Expect(func() {
				_ = m.Unprepare("claim-uid-123", drasriovtypes.PreparedDevices{})
			}).NotTo(Panic())
		})

		It("should not panic when preparedDevices is nil", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := &Manager{
				cdi: cdiHandler,
			}

			Expect(func() {
				_ = m.Unprepare("claim-uid-123", nil)
			}).NotTo(Panic())
		})

		It("should not panic when first prepared device entry is nil", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := &Manager{
				cdi: cdiHandler,
			}

			Expect(func() {
				_ = m.Unprepare("claim-uid-123", drasriovtypes.PreparedDevices{nil})
			}).NotTo(Panic())
		})
	})

	Context("SetRepublishCallback", func() {
		It("should set the republish callback", func() {
			m := &Manager{}
			Expect(m.republishCallback).To(BeNil())

			callback := func(ctx context.Context) error {
				return nil
			}

			m.SetRepublishCallback(callback)
			Expect(m.republishCallback).NotTo(BeNil())
		})
	})

	Context("PrepareDevicesForClaim", func() {
		It("should return error when config decoding fails", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := &Manager{
				cdi:         cdiHandler,
				allocatable: drasriovtypes.AllocatableDevices{},
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Config: []resourceapi.DeviceAllocationConfiguration{
								{
									Source:   resourceapi.AllocationConfigSourceClass,
									Requests: []string{"req1"},
									DeviceConfiguration: resourceapi.DeviceConfiguration{
										Opaque: &resourceapi.OpaqueDeviceConfiguration{
											Driver: consts.DriverName,
											Parameters: runtime.RawExtension{
												Raw: []byte("invalid json"),
											},
										},
									},
								},
							},
						},
					},
				},
			}

			ifNameIndex := 0
			_, err = m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error creating map of opaque device config"))
		})

		It("should return standalone net-attach-def lookup errors from PrepareDevicesForClaim", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			k8sClientManager := newTestManagerWithK8sClient()

			m := &Manager{
				k8sClient:              k8sClientManager.k8sClient,
				cdi:                    cdiHandler,
				configurationMode:      string(consts.ConfigurationModeStandalone),
				allocatable:            drasriovtypes.AllocatableDevices{},
				deviceInfoStore:        NewDeviceInfoStore(),
				defaultInterfacePrefix: "vfnet",
			}
			m.allocatable["device1"] = resourceapi.Device{
				Name: "device1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributePciAddress: {StringValue: ptr.To("0000:01:00.1")},
				},
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
									Pool:    "pool1",
								},
							},
							Config: []resourceapi.DeviceAllocationConfiguration{
								{
									Source:   resourceapi.AllocationConfigSourceClass,
									Requests: []string{"req1"},
									DeviceConfiguration: resourceapi.DeviceConfiguration{
										Opaque: &resourceapi.OpaqueDeviceConfiguration{
											Driver: consts.DriverName,
											Parameters: runtime.RawExtension{
												Raw: []byte(`{"apiVersion":"sriovnetwork.k8snetworkplumbingwg.io/v1alpha1","kind":"VfConfig","netAttachDefName":"missing-net"}`),
											},
										},
									},
								},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			ifNameIndex := 0
			_, err = m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error applying config on device"))
			Expect(err.Error()).To(ContainSubstring("error getting net attach def raw config"))
		})

		It("should return error when no devices are prepared for the claim", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			m := &Manager{
				cdi:         cdiHandler,
				allocatable: drasriovtypes.AllocatableDevices{},
			}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: "other.driver", Device: "device1", Request: "req1", Pool: "pool1"},
							},
						},
					},
				},
			}

			ifNameIndex := 0
			_, err = m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no prepared devices found for claim"))
		})

		It("should include rollback failure when sync fails after binding changes", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			fakeStore := &fakeDeviceInfoUtils{saveErr: fmt.Errorf("save failed")}
			m := &Manager{
				cdi:               cdiHandler,
				deviceInfoStore:   fakeStore,
				configurationMode: string(consts.ConfigurationModeMultus),
				allocatable: drasriovtypes.AllocatableDevices{
					"device1": {
						Name: "device1",
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress:         {StringValue: ptr.To("0000:01:00.1")},
							consts.AttributeMultusResourceName: {StringValue: ptr.To("intel.com/sriov")},
							consts.AttributeMultusDeviceID:     {StringValue: ptr.To("0000:01:00.1")},
						},
					},
				},
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", gomock.Any()).Return("ixgbevf", nil)
			mockHost.EXPECT().GetVFIODeviceFile("0000:01:00.1").Return("/dev/vfio/1", "/dev/vfio/1", nil)
			mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.1").Return([]string{})
			mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.1", "ixgbevf").Return(fmt.Errorf("restore failed"))

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: consts.DriverName, Device: "device1", Request: "req1", Pool: "pool1"},
							},
							Config: []resourceapi.DeviceAllocationConfiguration{
								{
									Source:   resourceapi.AllocationConfigSourceClass,
									Requests: []string{"req1"},
									DeviceConfiguration: resourceapi.DeviceConfiguration{
										Opaque: &resourceapi.OpaqueDeviceConfiguration{
											Driver: consts.DriverName,
											Parameters: runtime.RawExtension{
												Raw: []byte(`{"apiVersion":"sriovnetwork.k8snetworkplumbingwg.io/v1alpha1","kind":"VfConfig","driver":"vfio-pci"}`),
											},
										},
									},
								},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			ifNameIndex := 0
			_, err = m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unable to create device-info files for claim"))
			Expect(err.Error()).To(ContainSubstring("rollback failed"))
		})

		It("should include cleanup failure details when post-sync cleanup fails", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			fakeStore := &fakeDeviceInfoUtils{
				saveErr:   fmt.Errorf("save failed"),
				cleanErrs: []error{nil, fmt.Errorf("clean failed")},
			}
			m := &Manager{
				cdi:               cdiHandler,
				deviceInfoStore:   fakeStore,
				configurationMode: string(consts.ConfigurationModeMultus),
				allocatable: drasriovtypes.AllocatableDevices{
					"device1": {
						Name: "device1",
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress:         {StringValue: ptr.To("0000:01:00.1")},
							consts.AttributeMultusResourceName: {StringValue: ptr.To("intel.com/sriov")},
							consts.AttributeMultusDeviceID:     {StringValue: ptr.To("0000:01:00.1")},
						},
					},
				},
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", gomock.Any()).Return("", nil)
			mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.1").Return([]string{})

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: consts.DriverName, Device: "device1", Request: "req1", Pool: "pool1"},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			ifNameIndex := 0
			_, err = m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unable to create device-info files for claim"))
			Expect(err.Error()).To(ContainSubstring("cleanup after device-info sync failure failed"))
		})

		It("should use default config when no config found for driver", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := &Manager{
				cdi: cdiHandler,
				allocatable: drasriovtypes.AllocatableDevices{
					"device1": {
						Name: "device1",
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress: {
								StringValue: ptr.To("0000:01:00.1"),
							},
						},
					},
				},
				configurationMode: string(consts.ConfigurationModeMultus),
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
									Pool:    "pool1",
								},
							},
							Config: []resourceapi.DeviceAllocationConfiguration{
								// No config for our driver
								{
									Source:   resourceapi.AllocationConfigSourceClass,
									Requests: []string{"req1"},
									DeviceConfiguration: resourceapi.DeviceConfiguration{
										Opaque: &resourceapi.OpaqueDeviceConfiguration{
											Driver: "other.driver.com",
											Parameters: runtime.RawExtension{
												Raw: []byte(`{}`),
											},
										},
									},
								},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", gomock.Any()).Return("", nil)

			ifNameIndex := 0
			prepared, err := m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(prepared).To(HaveLen(1))
			Expect(prepared[0].NetAttachDefConfig).To(BeEmpty())
			Expect(claim.Status.Devices).To(HaveLen(1))
		})
	})

	Context("prepareDevices", func() {
		It("should skip devices for other drivers", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{},
			}

			vfConfig := &configapi.VfConfig{
				NetAttachDefName: "test-net",
			}

			claim := &resourceapi.ResourceClaim{
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  "other.driver.com",
									Device:  "device1",
									Request: "req1",
								},
							},
						},
					},
				},
			}

			resultsConfig := map[string]*configapi.VfConfig{
				"req1": vfConfig,
			}

			ifNameIndex := 0
			devices, err := m.prepareDevices(context.Background(), &ifNameIndex, claim, resultsConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
		})

		It("should use default config when config not found for request", func() {
			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := &Manager{
				cdi: cdiHandler,
				allocatable: drasriovtypes.AllocatableDevices{
					"device1": {
						Name: "device1",
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress: {
								StringValue: ptr.To("0000:01:00.1"),
							},
						},
					},
				},
				configurationMode: string(consts.ConfigurationModeMultus),
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
									Pool:    "pool1",
								},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			resultsConfig := map[string]*configapi.VfConfig{
				// Missing req1
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", gomock.Any()).Return("", nil)

			ifNameIndex := 0
			prepared, err := m.prepareDevices(context.Background(), &ifNameIndex, claim, resultsConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(prepared).To(HaveLen(1))
			Expect(prepared[0].IfName).To(Equal(""))
		})

		It("should return error when device not found in allocatable devices", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{
					// device1 not present
				},
			}

			vfConfig := &configapi.VfConfig{
				NetAttachDefName: "test-net",
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
									Pool:    "pool1",
								},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			resultsConfig := map[string]*configapi.VfConfig{
				"req1": vfConfig,
			}

			ifNameIndex := 0
			_, err := m.prepareDevices(context.Background(), &ifNameIndex, claim, resultsConfig)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error applying config on device"))
		})

		It("should successfully prepare devices and populate claim status", func() {
			netAttachDef := &netattdefv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-net",
					Namespace: "test-ns",
				},
				Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
					Config: `{"cniVersion":"0.3.1","type":"sriov"}`,
				},
			}

			cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			m := newTestManagerWithK8sClient(netAttachDef)
			m.cdi = cdiHandler
			m.defaultInterfacePrefix = "net"
			m.allocatable = drasriovtypes.AllocatableDevices{
				"device1": resourceapi.Device{
					Name: "device1",
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributePciAddress: {
							StringValue: ptr.To("0000:01:00.1"),
						},
					},
				},
			}

			vfConfig := &configapi.VfConfig{
				NetAttachDefName: "test-net",
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
									Pool:    "pool1",
								},
							},
						},
					},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			resultsConfig := map[string]*configapi.VfConfig{
				"req1": vfConfig,
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", vfConfig).Return("", nil)

			ifNameIndex := 0
			devices, err := m.prepareDevices(context.Background(), &ifNameIndex, claim, resultsConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))

			Expect(devices[0].PciAddress).To(Equal("0000:01:00.1"))
			Expect(devices[0].IfName).To(Equal("net0"))
			Expect(devices[0].PodUID).To(Equal("pod-uid"))
			Expect(devices[0].Device.DeviceName).To(Equal("device1"))
			Expect(devices[0].Device.PoolName).To(Equal("pool1"))
			Expect(devices[0].Device.RequestNames).To(Equal([]string{"req1"}))

			Expect(claim.Status.Devices).To(HaveLen(1))
			Expect(claim.Status.Devices[0].Device).To(Equal("device1"))
			Expect(claim.Status.Devices[0].Pool).To(Equal("pool1"))
			Expect(claim.Status.Devices[0].Driver).To(Equal(consts.DriverName))
		})
	})

	Context("applyConfigOnDevice", func() {
		It("should return error when device not found", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{},
			}

			config := &configapi.VfConfig{
				NetAttachDefName: "test-net",
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
				},
			}

			result := &resourceapi.DeviceRequestAllocationResult{
				Device: "nonexistent",
			}

			ifNameIndex := 0
			_, err := m.applyConfigOnDevice(context.Background(), &ifNameIndex, claim, config, result)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("device nonexistent not found"))
		})

		It("should use custom namespace from config", func() {
			netAttachDef := &netattdefv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-net",
					Namespace: "custom-ns",
				},
				Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
					Config: `{"cniVersion":"0.3.1","type":"sriov"}`,
				},
			}

			m := newTestManagerWithK8sClient(netAttachDef)
			m.defaultInterfacePrefix = "net"
			m.allocatable = drasriovtypes.AllocatableDevices{
				"device1": resourceapi.Device{
					Name: "device1",
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributePciAddress: {
							StringValue: ptr.To("0000:01:00.1"),
						},
					},
				},
			}

			config := &configapi.VfConfig{
				NetAttachDefName:      "test-net",
				NetAttachDefNamespace: "custom-ns",
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}

			result := &resourceapi.DeviceRequestAllocationResult{
				Device:  "device1",
				Request: "req1",
				Pool:    "pool1",
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", config).Return("", nil)

			ifNameIndex := 0
			preparedDevice, err := m.applyConfigOnDevice(context.Background(), &ifNameIndex, claim, config, result)
			Expect(err).NotTo(HaveOccurred())
			Expect(preparedDevice).NotTo(BeNil())
			Expect(preparedDevice.PciAddress).To(Equal("0000:01:00.1"))
			Expect(preparedDevice.IfName).To(Equal("net0"))
		})

		It("restores the original driver when VFIO file lookup fails", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{
					"device1": {
						Name: "device1",
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress: {StringValue: ptr.To("0000:01:00.1")},
						},
					},
				},
				configurationMode: string(consts.ConfigurationModeMultus),
			}
			config := &configapi.VfConfig{
				Driver: "vfio-pci",
			}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "test-ns",
					UID:       "claim-uid",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{UID: "pod-uid"},
					},
				},
			}
			result := &resourceapi.DeviceRequestAllocationResult{
				Device:  "device1",
				Request: "req1",
				Pool:    "pool1",
			}

			mockHost.EXPECT().BindDeviceDriver("0000:01:00.1", config).Return("ixgbevf", nil)
			mockHost.EXPECT().GetVFIODeviceFile("0000:01:00.1").Return("", "", fmt.Errorf("vfio lookup failed"))
			mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.1", "ixgbevf").Return(nil)

			ifNameIndex := 0
			_, err := m.applyConfigOnDevice(context.Background(), &ifNameIndex, claim, config, result)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error getting VFIO device file"))
		})
	})

	Context("UpdatePolicyDevices", func() {
		It("advertises devices present in the map and applies attributes", func() {
			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}},
					"devB": {Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}},
				},
			}

			resName := "vendor.com/resA"
			policyDevices := map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"devA": {
					consts.AttributeResourceName: {StringValue: &resName},
				},
			}
			err := s.UpdatePolicyDevices(context.Background(), policyDevices)
			Expect(err).ToNot(HaveOccurred())

			Expect(s.policyAttrKeys).To(HaveKey("devA"))
			Expect(s.policyAttrKeys).ToNot(HaveKey("devB"))

			val := s.allocatable["devA"].Attributes[consts.AttributeResourceName].StringValue
			Expect(val).ToNot(BeNil())
			Expect(*val).To(Equal("vendor.com/resA"))
		})

		It("clears policy attributes when device is removed from map", func() {
			resName := "vendor.com/resA"
			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributeResourceName: {StringValue: &resName},
						consts.AttributeVendorID:     {StringValue: ptr.To("8086")},
					}},
				},
				policyAttrKeys: map[string]map[resourceapi.QualifiedName]bool{
					"devA": {consts.AttributeResourceName: true},
				},
			}

			// Remove devA from policy
			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{})
			Expect(err).ToNot(HaveOccurred())

			Expect(s.policyAttrKeys).To(BeEmpty())
			// Policy attribute (resourceName) should be cleared
			_, exists := s.allocatable["devA"].Attributes[consts.AttributeResourceName]
			Expect(exists).To(BeFalse())
			// Discovery attribute (vendorID) should still exist
			_, exists = s.allocatable["devA"].Attributes[consts.AttributeVendorID]
			Expect(exists).To(BeTrue())
		})

		It("GetAdvertisedDevices returns only advertised devices", func() {
			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {},
					"devB": {},
				},
				policyAttrKeys: map[string]map[resourceapi.QualifiedName]bool{
					"devA": {},
				},
			}

			advertised := s.GetAdvertisedDevices()
			Expect(advertised).To(HaveLen(1))
			Expect(advertised).To(HaveKey("devA"))
		})

		It("applies policy attributes to PCI inventory-only devices", func() {
			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}},
				},
				pciAddressInventory: map[string]resourceapi.Device{
					"devV": {Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributePciAddress: {StringValue: ptr.To("0000:af:00.5")},
					}},
				},
			}
			resName := "vendor.com/inventory"
			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"devV": {
					consts.AttributeResourceName: {StringValue: &resName},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(s.policyAttrKeys).To(HaveKey("devV"))
			_, hasResourceName := s.pciAddressInventory["devV"].Attributes[resourceapi.QualifiedName(consts.AttributeResourceName)]
			Expect(hasResourceName).To(BeTrue())
			Expect(s.GetAdvertisedDevices()).To(HaveKey("devV"))
		})

		It("clears policy attributes from PCI inventory-only devices", func() {
			resName := "vendor.com/inventory"
			s := &Manager{
				pciAddressInventory: map[string]resourceapi.Device{
					"devV": {Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributeResourceName: {StringValue: &resName},
						consts.AttributePciAddress:   {StringValue: ptr.To("0000:af:00.5")},
					}},
				},
				policyAttrKeys: map[string]map[resourceapi.QualifiedName]bool{
					"devV": {consts.AttributeResourceName: true},
				},
			}

			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{})
			Expect(err).ToNot(HaveOccurred())
			Expect(s.policyAttrKeys).To(BeEmpty())
			_, exists := s.pciAddressInventory["devV"].Attributes[consts.AttributeResourceName]
			Expect(exists).To(BeFalse())
			_, keepsDiscovery := s.pciAddressInventory["devV"].Attributes[consts.AttributePciAddress]
			Expect(keepsDiscovery).To(BeTrue())
		})

		It("should trigger republish callback when changes are made", func() {
			callbackCalled := false
			callback := func(ctx context.Context) error {
				callbackCalled = true
				return nil
			}

			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {},
				},
				republishCallback: callback,
			}

			resName := "vendor.com/resA"
			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"devA": {
					consts.AttributeResourceName: {StringValue: &resName},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
		})

		It("should trigger republish callback when clearing advertised device with no policy attributes", func() {
			callbackCalled := false
			callback := func(ctx context.Context) error {
				callbackCalled = true
				return nil
			}

			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {},
				},
				policyAttrKeys: map[string]map[resourceapi.QualifiedName]bool{
					"devA": {},
				},
				republishCallback: callback,
			}

			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			Expect(s.GetAdvertisedDevices()).To(BeEmpty())
		})

		It("should not trigger callback when no changes are made", func() {
			callbackCalled := false
			callback := func(ctx context.Context) error {
				callbackCalled = true
				return nil
			}

			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributeResourceName: {
								StringValue: ptr.To("vendor.com/resA"),
							},
						},
					},
				},
				policyAttrKeys: map[string]map[resourceapi.QualifiedName]bool{
					"devA": {consts.AttributeResourceName: true},
				},
				republishCallback: callback,
			}

			resName := "vendor.com/resA"
			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"devA": {
					consts.AttributeResourceName: {StringValue: &resName},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeFalse())
		})

		It("should return error when republish callback fails", func() {
			callback := func(ctx context.Context) error {
				return fmt.Errorf("republish failed")
			}

			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {},
				},
				republishCallback: callback,
			}

			resName := "vendor.com/resA"
			err := s.UpdatePolicyDevices(context.Background(), map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"devA": {
					consts.AttributeResourceName: {StringValue: &resName},
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to republish resources"))
		})
	})

	Context("RDMA Device Preparation", func() {
		It("should skip RDMA preparation when device is not RDMA capable", func() {
			manager := &Manager{}
			nonRdmaDevice := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(false)},
				},
			}

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), nonRdmaDevice, "0000:08:00.1", "device-1")
			Expect(err).NotTo(HaveOccurred())
			Expect(deviceNodes).To(BeEmpty())
			Expect(envs).To(BeEmpty())
		})
	})

	Context("handleRDMADevice", func() {
		var (
			mockCtrl    *gomock.Controller
			mockHost    *mock_host.MockInterface
			origHelpers host.Interface
			manager     *Manager
		)

		BeforeEach(func() {
			mockCtrl = gomock.NewController(GinkgoT())
			mockHost = mock_host.NewMockInterface(mockCtrl)
			_ = host.GetHelpers()
			origHelpers = host.Helpers
			host.Helpers = mockHost

			manager = &Manager{}
		})

		AfterEach(func() {
			host.Helpers = origHelpers
			mockCtrl.Finish()
		})

		It("should return device nodes and environment variables for RDMA device", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"
			rdmaDeviceName := "mlx5_0"

			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{rdmaDeviceName})
			mockHost.EXPECT().GetRDMACharDevices(rdmaDeviceName).Return([]string{
				"/dev/infiniband/uverbs0",
				"/dev/infiniband/umad0",
				"/dev/infiniband/issm0",
				"/dev/infiniband/rdma_cm",
			}, nil)

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			Expect(err).ToNot(HaveOccurred())
			Expect(deviceNodes).To(HaveLen(4))
			Expect(deviceNodes[0].Path).To(Equal("/dev/infiniband/uverbs0"))
			Expect(deviceNodes[0].HostPath).To(Equal("/dev/infiniband/uverbs0"))
			Expect(deviceNodes[0].Type).To(Equal("c"))
			Expect(deviceNodes[1].Path).To(Equal("/dev/infiniband/umad0"))
			Expect(deviceNodes[2].Path).To(Equal("/dev/infiniband/issm0"))
			Expect(deviceNodes[3].Path).To(Equal("/dev/infiniband/rdma_cm"))

			Expect(envs).To(HaveLen(5))
			Expect(envs).To(ContainElement("SRIOVNETWORK_device_1_RDMA_UVERB=/dev/infiniband/uverbs0"))
			Expect(envs).To(ContainElement("SRIOVNETWORK_device_1_RDMA_UMAD=/dev/infiniband/umad0"))
			Expect(envs).To(ContainElement("SRIOVNETWORK_device_1_RDMA_ISSM=/dev/infiniband/issm0"))
			Expect(envs).To(ContainElement("SRIOVNETWORK_device_1_RDMA_CM=/dev/infiniband/rdma_cm"))
			Expect(envs).To(ContainElement("SRIOVNETWORK_device_1_RDMA_DEVICE=mlx5_0"))
		})

		It("should return error when multiple RDMA devices found", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"

			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{"mlx5_0", "mlx5_1"})

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("expected exactly one RDMA device"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})

		It("should return empty lists when device is not RDMA capable", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"

			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(false)},
				},
			}

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			Expect(err).ToNot(HaveOccurred())
			Expect(deviceNodes).To(BeEmpty())
			Expect(envs).To(BeEmpty())
		})

		It("should return error when no RDMA devices found", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"

			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{})

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no RDMA devices found"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})

		It("should return error when GetRDMACharDevices fails", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"
			rdmaDeviceName := "mlx5_0"

			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{rdmaDeviceName})
			mockHost.EXPECT().GetRDMACharDevices(rdmaDeviceName).Return(nil, fmt.Errorf("failed to get char devices"))

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to get char devices"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})

		It("should return error when no character devices found", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"
			rdmaDeviceName := "mlx5_0"

			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{rdmaDeviceName})
			mockHost.EXPECT().GetRDMACharDevices(rdmaDeviceName).Return([]string{}, nil)

			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no RDMA character devices found"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})
	})

	Context("MULTUS/STANDALONE behavior", func() {
		It("skips ifName generation and NetAttachDef fetch in MULTUS", func() {
			tmp, err := os.MkdirTemp("", "cdi-root")
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(tmp)
			cdiHandler, err := cdi.NewHandler(tmp)
			Expect(err).ToNot(HaveOccurred())

			s := &Manager{
				k8sClient:              flags.ClientSets{},
				defaultInterfacePrefix: "vfnet",
				cdi:                    cdiHandler,
				allocatable: drasriovtypes.AllocatableDevices{
					"devA": {
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress: {StringValue: strPtr("0000:00:00.1")},
						},
					},
				},
				configurationMode: string(consts.ConfigurationModeMultus),
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim1", Namespace: "ns1"},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("poduid-1")}},
				},
			}
			cfg := &configapi.VfConfig{NetAttachDefName: "nad1"} // should be ignored in MULTUS
			ifIndex := 0
			res := &resourceapi.DeviceRequestAllocationResult{Device: "devA", Pool: "pool1", Request: "req1"}
			mockHost.EXPECT().BindDeviceDriver("0000:00:00.1", cfg).Return("", nil)

			pd, err := s.applyConfigOnDevice(context.Background(), &ifIndex, claim, cfg, res)
			Expect(err).ToNot(HaveOccurred())
			Expect(pd).ToNot(BeNil())
			// ifName should remain empty and index unchanged
			Expect(pd.IfName).To(Equal(""))
			Expect(ifIndex).To(Equal(0))
			// NetAttachDefConfig should be empty
			Expect(pd.NetAttachDefConfig).To(BeEmpty())
		})

		It("allows VFIO in STANDALONE mode without netAttachDefName", func() {
			tmp, err := os.MkdirTemp("", "cdi-root")
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(tmp)
			cdiHandler, err := cdi.NewHandler(tmp)
			Expect(err).ToNot(HaveOccurred())

			s := &Manager{
				k8sClient:              flags.ClientSets{},
				defaultInterfacePrefix: "vfnet",
				cdi:                    cdiHandler,
				allocatable: drasriovtypes.AllocatableDevices{
					"devA": {
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress: {StringValue: strPtr("0000:00:00.1")},
						},
					},
				},
				configurationMode: string(consts.ConfigurationModeStandalone),
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim1", Namespace: "ns1"},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("poduid-1")}},
				},
			}
			cfg := &configapi.VfConfig{Driver: "vfio-pci"}
			ifIndex := 0
			res := &resourceapi.DeviceRequestAllocationResult{Device: "devA", Pool: "pool1", Request: "req1"}
			mockHost.EXPECT().BindDeviceDriver("0000:00:00.1", cfg).Return("", nil)
			mockHost.EXPECT().GetVFIODeviceFile("0000:00:00.1").Return("/dev/vfio/10", "/dev/vfio/10", nil)

			pd, err := s.applyConfigOnDevice(context.Background(), &ifIndex, claim, cfg, res)
			Expect(err).ToNot(HaveOccurred())
			Expect(pd).ToNot(BeNil())
			Expect(pd.NetAttachDefConfig).To(BeEmpty())
			Expect(pd.IfName).To(BeEmpty())
			Expect(ifIndex).To(Equal(0))
			Expect(pd.ContainerEdits.ContainerEdits.Env).ToNot(ContainElement(ContainSubstring("SRIOVNETWORK_NET_ATTACH_DEF_NAME=")))
		})

		It("requires netAttachDefName for non-VFIO standalone workloads", func() {
			s := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{
					"devA": {
						Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
							consts.AttributePciAddress: {StringValue: strPtr("0000:00:00.1")},
						},
					},
				},
				configurationMode: string(consts.ConfigurationModeStandalone),
			}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim1", Namespace: "ns1"},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("poduid-1")}},
				},
			}
			cfg := &configapi.VfConfig{Driver: "default"}
			ifIndex := 0
			res := &resourceapi.DeviceRequestAllocationResult{Device: "devA", Pool: "pool1", Request: "req1"}

			_, err := s.applyConfigOnDevice(context.Background(), &ifIndex, claim, cfg, res)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("net attach def name must be set in STANDALONE mode unless driver is vfio-pci"))
		})

		It("does not consume standalone ifName index for VFIO no-CNI allocations", func() {
			netAttachDef := &netattdefv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-net",
					Namespace: "ns1",
				},
				Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
					Config: `{"cniVersion":"1.0.0","type":"host-device","capabilities":{"deviceID":true}}`,
				},
			}
			s := newTestManagerWithK8sClient(netAttachDef)
			s.defaultInterfacePrefix = "vfnet"
			s.allocatable = drasriovtypes.AllocatableDevices{
				"devA": {
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributePciAddress: {StringValue: strPtr("0000:00:00.1")},
					},
				},
				"devB": {
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						consts.AttributePciAddress: {StringValue: strPtr("0000:00:00.2")},
					},
				},
			}
			s.configurationMode = string(consts.ConfigurationModeStandalone)

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim1", Namespace: "ns1", UID: "claim-uid"},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("poduid-1")}},
				},
			}
			ifIndex := 0
			vfioCfg := &configapi.VfConfig{Driver: consts.VFIODriverName}
			kernelCfg := &configapi.VfConfig{Driver: "default", NetAttachDefName: "test-net"}
			vfioRes := &resourceapi.DeviceRequestAllocationResult{Device: "devA", Pool: "pool1", Request: "req1"}
			kernelRes := &resourceapi.DeviceRequestAllocationResult{Device: "devB", Pool: "pool1", Request: "req2"}

			mockHost.EXPECT().BindDeviceDriver("0000:00:00.1", vfioCfg).Return("", nil)
			mockHost.EXPECT().GetVFIODeviceFile("0000:00:00.1").Return("/dev/vfio/10", "/dev/vfio/10", nil)
			mockHost.EXPECT().BindDeviceDriver("0000:00:00.2", kernelCfg).Return("", nil)

			vfioDevice, err := s.applyConfigOnDevice(context.Background(), &ifIndex, claim, vfioCfg, vfioRes)
			Expect(err).ToNot(HaveOccurred())
			Expect(vfioDevice.IfName).To(BeEmpty())
			Expect(ifIndex).To(Equal(0))

			kernelDevice, err := s.applyConfigOnDevice(context.Background(), &ifIndex, claim, kernelCfg, kernelRes)
			Expect(err).ToNot(HaveOccurred())
			Expect(kernelDevice.IfName).To(Equal("vfnet0"))
			Expect(ifIndex).To(Equal(1))
		})
	})
})

func strPtr(s string) *string { return &s }
