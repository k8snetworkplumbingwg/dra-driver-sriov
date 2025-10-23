package devicestate

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configapi "github.com/SchSeba/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/SchSeba/dra-driver-sriov/pkg/cdi"
	"github.com/SchSeba/dra-driver-sriov/pkg/consts"
	"github.com/SchSeba/dra-driver-sriov/pkg/flags"
	drasriovtypes "github.com/SchSeba/dra-driver-sriov/pkg/types"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
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
							"sriovnetwork.openshift.io/pciAddress": {StringValue: strPtr("0000:00:00.1")},
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

			pd, err := s.applyConfigOnDevice(context.Background(), &ifIndex, claim, cfg, res)
			Expect(err).ToNot(HaveOccurred())
			Expect(pd).ToNot(BeNil())
			// ifName should remain empty and index unchanged
			Expect(pd.IfName).To(Equal(""))
			Expect(ifIndex).To(Equal(0))
			// NetAttachDefConfig should be empty
			Expect(pd.NetAttachDefConfig).To(BeEmpty())
		})
	})
})

func strPtr(s string) *string { return &s }
