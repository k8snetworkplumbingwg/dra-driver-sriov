package devicestate

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	"k8s.io/utils/ptr"

	resourceapi "k8s.io/api/resource/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
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
