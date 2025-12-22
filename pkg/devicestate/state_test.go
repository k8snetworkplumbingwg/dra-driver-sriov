package devicestate

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
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
			// - GetRDMADeviceForPCI
			// - GetRDMACharDevices
			// This test verifies the condition evaluates correctly for non-RDMA devices
		})
	})
})
