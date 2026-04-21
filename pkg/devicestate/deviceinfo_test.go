package devicestate

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/utils/ptr"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

type deviceInfoCleanCall struct {
	resourceName string
	deviceID     string
}

type deviceInfoSaveCall struct {
	resourceName string
	deviceID     string
	devInfo      *nettypes.DeviceInfo
}

type fakeDeviceInfoUtils struct {
	cleanCalls []deviceInfoCleanCall
	saveCalls  []deviceInfoSaveCall
	cleanErrs  []error
	saveErrs   []error
	cleanErr   error
	saveErr    error
}

// CleanDeviceInfoForDP records cleanup calls for assertions.
func (f *fakeDeviceInfoUtils) CleanDeviceInfoForDP(resourceName, deviceID string) error {
	f.cleanCalls = append(f.cleanCalls, deviceInfoCleanCall{resourceName: resourceName, deviceID: deviceID})
	if len(f.cleanErrs) > 0 {
		nextErr := f.cleanErrs[0]
		f.cleanErrs = f.cleanErrs[1:]
		return nextErr
	}
	return f.cleanErr
}

// SaveDeviceInfoForDP records save calls for assertions.
func (f *fakeDeviceInfoUtils) SaveDeviceInfoForDP(resourceName, deviceID string, devInfo *nettypes.DeviceInfo) error {
	f.saveCalls = append(f.saveCalls, deviceInfoSaveCall{resourceName: resourceName, deviceID: deviceID, devInfo: devInfo})
	if len(f.saveErrs) > 0 {
		nextErr := f.saveErrs[0]
		f.saveErrs = f.saveErrs[1:]
		return nextErr
	}
	return f.saveErr
}

var _ = Describe("DeviceInfo compatibility", Serial, func() {
	var (
		mockCtrl    *gomock.Controller
		mockHost    *mock_host.MockInterface
		origHelpers host.Interface
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockHost = mock_host.NewMockInterface(mockCtrl)
		origHelpers = host.GetHelpers()
		host.Helpers = mockHost
	})

	AfterEach(func() {
		host.Helpers = origHelpers
		mockCtrl.Finish()
	})

	It("saves PCI device-info without RDMA details when no RDMA devices are present", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-1",
			},
			PciAddress:         "0000:01:00.1",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.1",
		}

		mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.1").Return([]string{})

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeUtils.cleanCalls).To(HaveLen(1))
		Expect(fakeUtils.saveCalls).To(HaveLen(1))
		Expect(fakeUtils.saveCalls[0].resourceName).To(Equal("intel.com/sriov"))
		Expect(fakeUtils.saveCalls[0].deviceID).To(Equal("0000:01:00.1"))
		Expect(fakeUtils.saveCalls[0].devInfo.Type).To(Equal(nettypes.DeviceInfoTypePCI))
		Expect(fakeUtils.saveCalls[0].devInfo.Version).To(Equal(nettypes.DeviceInfoVersion))
		Expect(fakeUtils.saveCalls[0].devInfo.Pci).NotTo(BeNil())
		Expect(fakeUtils.saveCalls[0].devInfo.Pci.PciAddress).To(Equal("0000:01:00.1"))
		Expect(fakeUtils.saveCalls[0].devInfo.Pci.RdmaDevice).To(BeEmpty())
	})

	It("saves RDMA device list in sriov-device-plugin-compatible format", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-2",
			},
			PciAddress:         "0000:01:00.2",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.2",
		}

		mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.2").Return([]string{"mlx5_0", "mlx5_1"})

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeUtils.saveCalls).To(HaveLen(1))
		Expect(fakeUtils.saveCalls[0].devInfo.Pci.RdmaDevice).To(Equal("mlx5_0,mlx5_1"))
	})

	It("serializes device-info using the expected network-status schema", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-6",
			},
			PciAddress:         "0000:01:00.6",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.6",
		}

		mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.6").Return([]string{"mlx5_2"})

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.saveCalls).To(HaveLen(1))

		raw, err := json.Marshal(fakeUtils.saveCalls[0].devInfo)
		Expect(err).NotTo(HaveOccurred())

		var payload map[string]interface{}
		err = json.Unmarshal(raw, &payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(payload["type"]).To(Equal("pci"))
		Expect(payload["version"]).To(Equal("1.1.0"))

		pciPayload, ok := payload["pci"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(pciPayload["pci-address"]).To(Equal("0000:01:00.6"))
		Expect(pciPayload["rdma-device"]).To(Equal("mlx5_2"))
	})

	It("skips writing device-info when Multus resourceName is missing", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-3",
			},
			PciAddress:     "0000:01:00.3",
			MultusDeviceID: "0000:01:00.3",
		}

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(BeEmpty())
		Expect(fakeUtils.saveCalls).To(BeEmpty())
	})

	It("skips writing device-info when Multus deviceID is missing", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-3",
			},
			PciAddress:         "0000:01:00.3",
			MultusResourceName: "intel.com/sriov",
		}

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(BeEmpty())
		Expect(fakeUtils.saveCalls).To(BeEmpty())
	})

	It("returns aggregated errors for invalid prepared device entries", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{deviceInfoStore: fakeUtils}
		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{
			nil,
			&drasriovtypes.PreparedDevice{
				Device: drapbv1.Device{
					DeviceName: "0000-01-00-b",
				},
				PciAddress:         "",
				MultusResourceName: "intel.com/sriov",
				MultusDeviceID:     "0000:01:00.b",
			},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("prepared device is nil"))
		Expect(err.Error()).To(ContainSubstring("PCI address is empty"))
	})

	It("returns error when DP device-info cleanup fails before save", func() {
		fakeUtils := &fakeDeviceInfoUtils{cleanErr: fmt.Errorf("clean failed")}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-c",
			},
			PciAddress:         "0000:01:00.c",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.c",
		}

		mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.c").Return([]string{})

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("clean stale device-info"))
		Expect(fakeUtils.cleanCalls).To(HaveLen(1))
		Expect(fakeUtils.saveCalls).To(BeEmpty())
	})

	It("returns error when DP device-info save fails", func() {
		fakeUtils := &fakeDeviceInfoUtils{saveErr: fmt.Errorf("save failed")}
		manager := &Manager{deviceInfoStore: fakeUtils}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-d",
			},
			PciAddress:         "0000:01:00.d",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.d",
		}

		mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.d").Return([]string{})

		err := manager.syncDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("save device-info"))
		Expect(fakeUtils.cleanCalls).To(HaveLen(1))
		Expect(fakeUtils.saveCalls).To(HaveLen(1))
	})

	It("returns error from PrepareDevicesForClaim when device-info sync fails in MULTUS mode", func() {
		fakeUtils := &fakeDeviceInfoUtils{saveErr: fmt.Errorf("save failed")}
		cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		manager := &Manager{
			cdi:               cdiHandler,
			deviceInfoStore:   fakeUtils,
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
		mockHost.EXPECT().RestoreDeviceDriver("0000:01:00.1", "ixgbevf").Return(nil)

		encodedConfig := []byte(`{"apiVersion":"sriovnetwork.k8snetworkplumbingwg.io/v1alpha1","kind":"VfConfig","driver":"vfio-pci"}`)

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
											Raw: encodedConfig,
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
		_, err = manager.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unable to create device-info files for claim"))
	})

	It("skips device-info sync in PrepareDevicesForClaim when configuration mode is not MULTUS", func() {
		fakeUtils := &fakeDeviceInfoUtils{saveErr: fmt.Errorf("save failed")}
		cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		netAttachDef := &netattdefv1.NetworkAttachmentDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-net",
				Namespace: "test-ns",
			},
			Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
				Config: `{"cniVersion":"0.3.1","type":"sriov"}`,
			},
		}
		k8sClientManager := newTestManagerWithK8sClient(netAttachDef)
		encodedConfig := []byte(`{"apiVersion":"sriovnetwork.k8snetworkplumbingwg.io/v1alpha1","kind":"VfConfig","netAttachDefName":"test-net"}`)

		manager := &Manager{
			k8sClient:         k8sClientManager.k8sClient,
			cdi:               cdiHandler,
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeStandalone),
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
											Raw: encodedConfig,
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
		_, err = manager.PrepareDevicesForClaim(context.Background(), &ifNameIndex, claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.saveCalls).To(BeEmpty())
	})

	It("cleans device-info files during Unprepare", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())

		manager := &Manager{
			cdi:               cdiHandler,
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeMultus),
		}

		preparedDevices := drasriovtypes.PreparedDevices{
			&drasriovtypes.PreparedDevice{
				Device: drapbv1.Device{
					DeviceName: "0000-01-00-4",
				},
				PciAddress:         "0000:01:00.4",
				MultusResourceName: "intel.com/sriov",
				MultusDeviceID:     "0000:01:00.4",
				PodUID:             "pod-uid",
				Config:             &configapi.VfConfig{},
			},
		}

		err = manager.Unprepare("claim-uid", preparedDevices)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(HaveLen(1))
		Expect(fakeUtils.cleanCalls[0].resourceName).To(Equal("intel.com/sriov"))
		Expect(fakeUtils.cleanCalls[0].deviceID).To(Equal("0000:01:00.4"))
	})

	It("returns error when device-info cleanup fails during Unprepare in MULTUS mode", func() {
		fakeUtils := &fakeDeviceInfoUtils{cleanErr: fmt.Errorf("clean failed")}
		cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())

		manager := &Manager{
			cdi:               cdiHandler,
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeMultus),
		}
		preparedDevices := drasriovtypes.PreparedDevices{
			&drasriovtypes.PreparedDevice{
				Device: drapbv1.Device{
					DeviceName: "0000-01-00-5",
				},
				PciAddress:         "0000:01:00.5",
				MultusResourceName: "intel.com/sriov",
				MultusDeviceID:     "0000:01:00.5",
				PodUID:             "pod-uid",
				Config:             &configapi.VfConfig{},
			},
		}

		err = manager.Unprepare("claim-uid", preparedDevices)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unable to clean device-info files for claim"))
	})

	It("skips device-info sync wrapper when configuration mode is not MULTUS", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeStandalone),
		}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-7",
			},
			PciAddress:         "0000:01:00.7",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.7",
		}

		err := manager.syncDeviceInfoFilesForPreparedDevicesIfNeeded(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(BeEmpty())
		Expect(fakeUtils.saveCalls).To(BeEmpty())
	})

	It("uses device-info sync wrapper when configuration mode is MULTUS", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeMultus),
		}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-8",
			},
			PciAddress:         "0000:01:00.8",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.8",
		}

		mockHost.EXPECT().GetRDMADevicesForPCI("0000:01:00.8").Return([]string{})

		err := manager.syncDeviceInfoFilesForPreparedDevicesIfNeeded(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(HaveLen(1))
		Expect(fakeUtils.saveCalls).To(HaveLen(1))
	})

	It("skips device-info cleanup wrapper when configuration mode is not MULTUS", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		manager := &Manager{
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeStandalone),
		}
		preparedDevice := &drasriovtypes.PreparedDevice{
			Device: drapbv1.Device{
				DeviceName: "0000-01-00-9",
			},
			PciAddress:         "0000:01:00.9",
			MultusResourceName: "intel.com/sriov",
			MultusDeviceID:     "0000:01:00.9",
		}

		err := manager.cleanDeviceInfoFilesForPreparedDevicesIfNeeded(context.Background(), drasriovtypes.PreparedDevices{preparedDevice})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(BeEmpty())
	})

	It("returns aggregated errors when cleanup gets invalid entries and cleanup failures", func() {
		fakeUtils := &fakeDeviceInfoUtils{cleanErr: fmt.Errorf("clean failed")}
		manager := &Manager{
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeMultus),
		}

		err := manager.cleanDeviceInfoFilesForPreparedDevices(context.Background(), drasriovtypes.PreparedDevices{
			nil,
			&drasriovtypes.PreparedDevice{
				Device: drapbv1.Device{
					DeviceName: "0000-01-00-e",
				},
				MultusResourceName: "intel.com/sriov",
				MultusDeviceID:     "0000:01:00.e",
			},
		})

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("prepared device is nil"))
		Expect(err.Error()).To(ContainSubstring("failed to clean device-info"))
	})

	It("skips device-info cleanup through Unprepare when configuration mode is not MULTUS", func() {
		fakeUtils := &fakeDeviceInfoUtils{}
		cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		manager := &Manager{
			cdi:               cdiHandler,
			deviceInfoStore:   fakeUtils,
			configurationMode: string(consts.ConfigurationModeStandalone),
		}

		preparedDevices := drasriovtypes.PreparedDevices{
			&drasriovtypes.PreparedDevice{
				Device: drapbv1.Device{
					DeviceName: "0000-01-00-a",
				},
				PciAddress:         "0000:01:00.1",
				MultusResourceName: "intel.com/sriov",
				MultusDeviceID:     "0000:01:00.1",
				PodUID:             "pod-uid",
				Config:             &configapi.VfConfig{},
			},
		}

		err = manager.Unprepare("claim-uid", preparedDevices)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeUtils.cleanCalls).To(BeEmpty())
	})
})
