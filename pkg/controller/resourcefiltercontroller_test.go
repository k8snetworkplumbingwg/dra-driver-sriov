package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	sriovconsts "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
	resourceapi "k8s.io/api/resource/v1"
)

// localFakeState implements devicestate.DeviceState with minimal logic for unit tests (same package access)
type localFakeState struct {
	alloc drasriovtypes.AllocatableDevices
}

func (l *localFakeState) GetAllocatableDevices() drasriovtypes.AllocatableDevices { return l.alloc }
func (l *localFakeState) UpdateDeviceResourceNames(_ context.Context, _ map[string]string) error {
	return nil
}

var _ = Describe("matchesNodeSelector", func() {
	It("handles empty, subset, and mismatch correctly", func() {
		r := &SriovResourceFilterReconciler{}
		node := map[string]string{"role": "dpdk", "zone": "a"}
		Expect(r.matchesNodeSelector(node, map[string]string{})).To(BeTrue())
		Expect(r.matchesNodeSelector(node, map[string]string{"role": "dpdk"})).To(BeTrue())
		Expect(r.matchesNodeSelector(node, map[string]string{"role": "gpu"})).To(BeFalse())
	})
})

var _ = Describe("stringSliceContains", func() {
	It("returns expected presence results", func() {
		r := &SriovResourceFilterReconciler{}
		Expect(r.stringSliceContains([]string{"a", "b"}, "c")).To(BeFalse())
		Expect(r.stringSliceContains([]string{"a", "b"}, "b")).To(BeTrue())
	})
})

var _ = Describe("deviceMatchesFilter", func() {
	It("matches valid filters and rejects mismatches", func() {
		r := &SriovResourceFilterReconciler{}
		vendor := "8086"
		dev := "154c"
		pf := "eth0"
		pci := "0000:00:00.1"
		root := "0000:00:00.0"
		numa := int64(0)
		d := resourceapi.Device{
			Name: "devA",
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				sriovconsts.AttributeVendorID:         {StringValue: &vendor},
				sriovconsts.AttributeDeviceID:         {StringValue: &dev},
				sriovconsts.AttributePFName:           {StringValue: &pf},
				sriovconsts.AttributePciAddress:       {StringValue: &pci},
				sriovconsts.AttributeParentPciAddress: {StringValue: &root},
				sriovconsts.AttributeNumaNode:         {IntValue: &numa},
			},
		}

		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{})).To(BeTrue())

		f := sriovdrav1alpha1.ResourceFilter{
			Vendors:      []string{"8086"},
			Devices:      []string{"154c"},
			PciAddresses: []string{"0000:00:00.1"},
			PfNames:      []string{"eth0"},
			RootDevices:  []string{"0000:00:00.0"},
			NumaNodes:    []string{"0"},
		}
		Expect(r.deviceMatchesFilter(d, f)).To(BeTrue())

		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{Vendors: []string{"1234"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{Devices: []string{"9999"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:00:00.2"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PfNames: []string{"eth9"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{RootDevices: []string{"0000:00:ff.f"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{NumaNodes: []string{"2"}})).To(BeFalse())
	})
})

var _ = Describe("getFilteredDeviceResourceMap", func() {
	It("assigns devices per first-match and skips empty resource names", func() {
		vendor := "8086"
		dev := "154c"
		alloc := drasriovtypes.AllocatableDevices{
			"devA": resourceapi.Device{
				Name: "devA",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID: {StringValue: &vendor},
					sriovconsts.AttributeDeviceID: {StringValue: &dev},
				},
			},
			"devB": resourceapi.Device{
				Name: "devB",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID: {StringValue: &vendor},
					sriovconsts.AttributeDeviceID: {StringValue: &dev},
				},
			},
		}
		r := &SriovResourceFilterReconciler{deviceStateManager: &localFakeState{alloc: alloc}}

		m := r.getFilteredDeviceResourceMap()
		Expect(m).To(BeEmpty())

		r.currentResourceFilter = &sriovdrav1alpha1.SriovResourceFilter{
			Spec: sriovdrav1alpha1.SriovResourceFilterSpec{
				Configs: []sriovdrav1alpha1.Config{
					{ResourceName: "resA", ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}}},
					{ResourceName: "", ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Devices: []string{"154c"}}}},
					{ResourceName: "resB", ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Devices: []string{"154c"}}}},
				},
			},
		}
		m = r.getFilteredDeviceResourceMap()
		Expect(m).To(HaveLen(2))
		Expect(m["devA"]).To(Equal("resA"))
		Expect(m["devB"]).To(Equal("resA"))
	})
})
