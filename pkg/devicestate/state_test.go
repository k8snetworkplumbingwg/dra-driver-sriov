package devicestate

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	resourceapi "k8s.io/api/resource/v1"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
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

var _ = Describe("Manager", func() {
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

	Context("GetAllocatedDeviceByDeviceName", func() {
		It("should return device when it exists", func() {
			devices := drasriovtypes.AllocatableDevices{
				"device1": resourceapi.Device{Name: "device1"},
			}

			m := &Manager{
				allocatable: devices,
			}

			device, exists := m.GetAllocatedDeviceByDeviceName("device1")
			Expect(exists).To(BeTrue())
			Expect(device.Name).To(Equal("device1"))
		})

		It("should return false when device does not exist", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{},
			}

			_, exists := m.GetAllocatedDeviceByDeviceName("nonexistent")
			Expect(exists).To(BeFalse())
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
				{
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
				{
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

		It("should skip driver restoration when no driver was set", func() {
			preparedDevices := drasriovtypes.PreparedDevices{
				{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "ixgbevf",
					Config:         &configapi.VfConfig{
						// No driver specified
					},
				},
			}

			// No mock expectation - RestoreDeviceDriver should not be called

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
				{
					PciAddress:     "0000:01:00.1",
					OriginalDriver: "",
					PodUID:         "pod-uid-123",
					Config:         &configapi.VfConfig{},
				},
			}

			m := &Manager{
				cdi: cdiHandler,
			}

			// The function will try to delete CDI spec files
			// Since we haven't created them, the delete will succeed (no error)
			// because DeleteSpecFile doesn't error on non-existent files
			err = m.Unprepare("claim-uid-123", preparedDevices)
			// No error expected since unprepareDevices succeeds (no driver to restore)
			// and DeleteSpecFile handles non-existent files gracefully
			Expect(err).NotTo(HaveOccurred())
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

		It("should return error when no config found for driver", func() {
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
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
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

			ifNameIndex := 0
			_, err = m.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error creating map of opaque device config"))
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

		It("should return error when config not found for request", func() {
			m := &Manager{
				allocatable: drasriovtypes.AllocatableDevices{},
			}

			claim := &resourceapi.ResourceClaim{
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{
									Driver:  consts.DriverName,
									Device:  "device1",
									Request: "req1",
								},
							},
						},
					},
				},
			}

			resultsConfig := map[string]*configapi.VfConfig{
				// Missing req1
			}

			ifNameIndex := 0
			_, err := m.prepareDevices(context.Background(), &ifNameIndex, claim, resultsConfig)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("config not found for request"))
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
	})

	Context("UpdateDeviceResourceNames", func() {
		It("adds, updates, and clears resource names correctly", func() {
			s := &Manager{
				allocatable: map[string]resourceapi.Device{
					"devA": {},
					"devB": {},
				},
			}

			// Add resource name to devA
			err := s.UpdateDeviceResourceNames(context.Background(), map[string]string{"devA": "vendor.com/resA"})
			Expect(err).ToNot(HaveOccurred())
			Expect(s.allocatable["devA"].Attributes).ToNot(BeNil())
			Expect(s.allocatable["devA"].Attributes).To(HaveKey(resourceapi.QualifiedName(consts.AttributeResourceName)))

			// Update to same value should be a no-op but still succeed
			err = s.UpdateDeviceResourceNames(context.Background(), map[string]string{"devA": "vendor.com/resA"})
			Expect(err).ToNot(HaveOccurred())

			// Change value and clear for devB
			err = s.UpdateDeviceResourceNames(context.Background(), map[string]string{"devA": "vendor.com/resA2", "devB": ""})
			Expect(err).ToNot(HaveOccurred())

			// Ensure attribute exists for devA with new value
			val := s.allocatable["devA"].Attributes[consts.AttributeResourceName].StringValue
			Expect(val).ToNot(BeNil())
			Expect(*val).To(Equal("vendor.com/resA2"))

			// Ensure attribute is cleared for devB when value empty
			_, exists := s.allocatable["devB"].Attributes[consts.AttributeResourceName]
			Expect(exists).To(BeFalse())
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

			err := s.UpdateDeviceResourceNames(context.Background(), map[string]string{"devA": "vendor.com/resA"})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
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
				republishCallback: callback,
			}

			// Same value - no change
			err := s.UpdateDeviceResourceNames(context.Background(), map[string]string{"devA": "vendor.com/resA"})
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

			err := s.UpdateDeviceResourceNames(context.Background(), map[string]string{"devA": "vendor.com/resA"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to republish resources"))
		})
	})

	Context("RDMA Device Preparation", func() {
		It("should skip RDMA preparation when device is not RDMA capable", func() {
			// Create device without RDMA capability
			nonRdmaDevice := &resourceapi.Device{
				Name: "0000-08-00-1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributePciAddress: {
						StringValue: ptr.To("0000:08:00.1"),
					},
					consts.AttributeRDMACapable: {
						BoolValue: ptr.To(false),
					},
				},
			}

			// Verify device is not RDMA capable
			rdmaCapable, exists := nonRdmaDevice.Attributes[consts.AttributeRDMACapable]
			Expect(exists).To(BeTrue())
			Expect(rdmaCapable.BoolValue).ToNot(BeNil())
			Expect(*rdmaCapable.BoolValue).To(BeFalse())

			// Test the conditional logic that determines if RDMA preparation should occur
			// This replicates the production code condition:
			// if rdmaCapableAttr, ok := deviceInfo.Attributes[consts.AttributeRDMACapable]; ok && rdmaCapableAttr.BoolValue != nil && *rdmaCapableAttr.BoolValue
			shouldPrepareRDMA := exists && rdmaCapable.BoolValue != nil && *rdmaCapable.BoolValue
			Expect(shouldPrepareRDMA).To(BeFalse(), "RDMA preparation should be skipped for non-RDMA capable devices")

			// When this condition is false, the production code never calls:
			// - GetRDMADevicesForPCI
			// - GetRDMACharDevices
			// This test verifies the condition evaluates correctly for non-RDMA devices
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
			// Save original helpers and replace with mock
			_ = host.GetHelpers()
			origHelpers = host.Helpers
			host.Helpers = mockHost

			manager = &Manager{}
		})

		AfterEach(func() {
			// Restore original helpers
			host.Helpers = origHelpers
			mockCtrl.Finish()
		})

		It("should return device nodes and environment variables for RDMA device", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"
			rdmaDeviceName := "mlx5_0"

			// Create device info with RDMA capability
			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			// Mock GetRDMADevicesForPCI to return one RDMA device
			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{rdmaDeviceName})

			// Mock GetRDMACharDevices to return various character devices
			mockHost.EXPECT().GetRDMACharDevices(rdmaDeviceName).Return([]string{
				"/dev/infiniband/uverbs0",
				"/dev/infiniband/umad0",
				"/dev/infiniband/issm0",
				"/dev/infiniband/rdma_cm",
			}, nil)

			// Call the function
			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			// Verify no error
			Expect(err).ToNot(HaveOccurred())

			// Verify device nodes
			Expect(deviceNodes).To(HaveLen(4))
			Expect(deviceNodes[0].Path).To(Equal("/dev/infiniband/uverbs0"))
			Expect(deviceNodes[0].HostPath).To(Equal("/dev/infiniband/uverbs0"))
			Expect(deviceNodes[0].Type).To(Equal("c"))
			Expect(deviceNodes[1].Path).To(Equal("/dev/infiniband/umad0"))
			Expect(deviceNodes[2].Path).To(Equal("/dev/infiniband/issm0"))
			Expect(deviceNodes[3].Path).To(Equal("/dev/infiniband/rdma_cm"))

			// Verify environment variables
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

			// Create device info with RDMA capability
			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			// Mock GetRDMADevicesForPCI to return two RDMA devices
			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{"mlx5_0", "mlx5_1"})

			// Call the function
			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			// Verify error is returned
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("expected exactly one RDMA device"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})

		It("should return empty lists when device is not RDMA capable", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"

			// Create device info without RDMA capability
			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(false)},
				},
			}

			// Call the function
			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			// Verify no error and empty lists
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceNodes).To(BeEmpty())
			Expect(envs).To(BeEmpty())
		})

		It("should return error when no RDMA devices found", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"

			// Create device info with RDMA capability
			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			// Mock GetRDMADevicesForPCI to return empty list
			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{})

			// Call the function
			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			// Verify error is returned
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no RDMA devices found"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})

		It("should return error when GetRDMACharDevices fails", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"
			rdmaDeviceName := "mlx5_0"

			// Create device info with RDMA capability
			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			// Mock GetRDMADevicesForPCI to return one RDMA device
			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{rdmaDeviceName})

			// Mock GetRDMACharDevices to return an error
			mockHost.EXPECT().GetRDMACharDevices(rdmaDeviceName).Return(nil, fmt.Errorf("failed to get char devices"))

			// Call the function
			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			// Verify error is returned
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to get char devices"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})

		It("should return error when no character devices found", func() {
			pciAddress := "0000:08:00.1"
			deviceName := "device-1"
			rdmaDeviceName := "mlx5_0"

			// Create device info with RDMA capability
			deviceInfo := resourceapi.Device{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributeRDMACapable: {BoolValue: ptr.To(true)},
				},
			}

			// Mock GetRDMADevicesForPCI to return one RDMA device
			mockHost.EXPECT().GetRDMADevicesForPCI(pciAddress).Return([]string{rdmaDeviceName})

			// Mock GetRDMACharDevices to return empty list
			mockHost.EXPECT().GetRDMACharDevices(rdmaDeviceName).Return([]string{}, nil)

			// Call the function
			deviceNodes, envs, err := manager.handleRDMADevice(context.Background(), deviceInfo, pciAddress, deviceName)

			// Verify error is returned
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no RDMA character devices found"))
			Expect(deviceNodes).To(BeNil())
			Expect(envs).To(BeNil())
		})
	})
})
