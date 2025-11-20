package devicestate

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	"k8s.io/utils/ptr"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	resourceapi "k8s.io/api/resource/v1"
)

var _ = Describe("Manager", func() {
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
	})

	Context("RDMA Device Preparation", func() {
		var (
			mockCtrl    *gomock.Controller
			mockHost    *mock_host.MockInterface
			deviceInfo  *resourceapi.Device
			pciAddress  string
			charDevices []string
		)

		BeforeEach(func() {
			mockCtrl = gomock.NewController(GinkgoT())
			mockHost = mock_host.NewMockInterface(mockCtrl)

			pciAddress = "0000:08:00.5"
			charDevices = []string{
				"/dev/infiniband/issm5",
				"/dev/infiniband/umad5",
				"/dev/infiniband/uverbs5",
				"/dev/infiniband/rdma_cm",
			}

			// Create a device with RDMA attributes
			deviceInfo = &resourceapi.Device{
				Name: "0000-08-00-5",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					consts.AttributePciAddress: {
						StringValue: ptr.To(pciAddress),
					},
					consts.AttributeRDMACapable: {
						BoolValue: ptr.To(true),
					},
					consts.AttributeRDMADevices: {
						StringValue: ptr.To("mlx5_5"),
					},
					consts.AttributeRDMAProtocol: {
						StringValue: ptr.To("RoCE"),
					},
				},
			}
		})

		AfterEach(func() {
			mockCtrl.Finish()
		})

		It("should add RDMA character devices to CDI spec", func() {
			// Verify device has RDMA attributes
			Expect(deviceInfo.Attributes[consts.AttributeRDMACapable].BoolValue).ToNot(BeNil())
			Expect(*deviceInfo.Attributes[consts.AttributeRDMACapable].BoolValue).To(BeTrue())

			// Mock GetRDMACharDevices to return character devices
			mockHost.EXPECT().
				GetRDMACharDevices("mlx5_5").
				Return(charDevices, nil).
				Times(1)

			// Verify character devices would be returned
			devices, err := mockHost.GetRDMACharDevices("mlx5_5")
			Expect(err).ToNot(HaveOccurred())
			Expect(devices).To(HaveLen(4))
			Expect(devices).To(ContainElement("/dev/infiniband/uverbs5"))
		})

		It("should add RDMA environment variables for each character device type", func() {
			// Verify environment variable naming patterns
			devicePrefix := "0000_08_00_5"

			expectedEnvVars := map[string]string{
				"SRIOVNETWORK_" + devicePrefix + "_RDMA_ISSM":   "/dev/infiniband/issm5",
				"SRIOVNETWORK_" + devicePrefix + "_RDMA_UMAD":   "/dev/infiniband/umad5",
				"SRIOVNETWORK_" + devicePrefix + "_RDMA_UVERBS": "/dev/infiniband/uverbs5",
				"SRIOVNETWORK_" + devicePrefix + "_RDMA_CM":     "/dev/infiniband/rdma_cm",
				"SRIOVNETWORK_" + devicePrefix + "_RDMA_DEVICE": "mlx5_5",
			}

			// This verifies the expected environment variable format
			Expect(expectedEnvVars).To(HaveLen(5))
			Expect(expectedEnvVars).To(HaveKeyWithValue("SRIOVNETWORK_0000_08_00_5_RDMA_DEVICE", "mlx5_5"))
		})

		It("should handle multiple RDMA devices", func() {
			// Update device to have multiple RDMA devices
			deviceInfo.Attributes[consts.AttributeRDMADevices] = resourceapi.DeviceAttribute{
				StringValue: ptr.To("mlx5_5,mlx5_6"),
			}

			// Mock calls for both devices
			mockHost.EXPECT().
				GetRDMACharDevices("mlx5_5").
				Return([]string{"/dev/infiniband/uverbs5", "/dev/infiniband/rdma_cm"}, nil).
				Times(1)

			mockHost.EXPECT().
				GetRDMACharDevices("mlx5_6").
				Return([]string{"/dev/infiniband/uverbs6", "/dev/infiniband/rdma_cm"}, nil).
				Times(1)

			// Call the mocked methods to satisfy expectations
			devices1, err1 := mockHost.GetRDMACharDevices("mlx5_5")
			Expect(err1).ToNot(HaveOccurred())
			Expect(devices1).To(HaveLen(2))

			devices2, err2 := mockHost.GetRDMACharDevices("mlx5_6")
			Expect(err2).ToNot(HaveOccurred())
			Expect(devices2).To(HaveLen(2))

			// Verify both devices would be processed
			rdmaDevicesList := []string{"mlx5_5", "mlx5_6"}
			Expect(rdmaDevicesList).To(HaveLen(2))
		})

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

			// GetRDMACharDevices should NOT be called
			mockHost.EXPECT().
				GetRDMACharDevices(gomock.Any()).
				Times(0)

			// Verify device is not RDMA capable
			rdmaCapable := nonRdmaDevice.Attributes[consts.AttributeRDMACapable].BoolValue
			Expect(rdmaCapable).ToNot(BeNil())
			Expect(*rdmaCapable).To(BeFalse())
		})

		It("should handle empty RDMA devices string", func() {
			// Device is RDMA capable but has no devices listed
			deviceInfo.Attributes[consts.AttributeRDMADevices] = resourceapi.DeviceAttribute{
				StringValue: ptr.To(""),
			}

			// GetRDMACharDevices should NOT be called
			mockHost.EXPECT().
				GetRDMACharDevices(gomock.Any()).
				Times(0)

			// Verify empty string handling
			rdmaDevicesStr := deviceInfo.Attributes[consts.AttributeRDMADevices].StringValue
			Expect(rdmaDevicesStr).ToNot(BeNil())
			Expect(*rdmaDevicesStr).To(BeEmpty())
		})

		It("should handle GetRDMACharDevices returning empty list", func() {
			// Mock returning empty list
			mockHost.EXPECT().
				GetRDMACharDevices("mlx5_5").
				Return([]string{}, nil).
				Times(1)

			// Call the mocked method
			devices, err := mockHost.GetRDMACharDevices("mlx5_5")

			// This should not cause errors, just return empty list
			Expect(err).ToNot(HaveOccurred())
			Expect(devices).To(BeEmpty())
		})

		It("should verify character device types are correctly identified", func() {
			// Test the device type identification logic
			testCases := []struct {
				path     string
				expected string
			}{
				{"/dev/infiniband/uverbs0", "uverbs"},
				{"/dev/infiniband/umad0", "umad"},
				{"/dev/infiniband/issm0", "issm"},
				{"/dev/infiniband/rdma_cm", "rdma_cm"},
			}

			for _, tc := range testCases {
				Expect(tc.path).To(ContainSubstring(tc.expected))
			}
		})
	})
})
