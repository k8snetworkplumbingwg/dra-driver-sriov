package controller

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	sriovconsts "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// localFakeState implements devicestate.DeviceState with minimal logic for unit tests (same package access)
type localFakeState struct {
	alloc               drasriovtypes.AllocatableDevices
	pciAddressInventory drasriovtypes.AllocatableDevices
	includePciCalls     []bool
}

func (l *localFakeState) GetAllocatableDevices() drasriovtypes.AllocatableDevices { return l.alloc }
func (l *localFakeState) GetPolicyCandidateDevices(includePciAddressInventory bool) drasriovtypes.AllocatableDevices {
	l.includePciCalls = append(l.includePciCalls, includePciAddressInventory)

	candidates := make(drasriovtypes.AllocatableDevices, len(l.alloc))
	for name, device := range l.alloc {
		candidates[name] = device
	}
	if !includePciAddressInventory {
		return candidates
	}
	for name, device := range l.pciAddressInventory {
		if _, exists := candidates[name]; !exists {
			candidates[name] = device
		}
	}
	return candidates
}
func (l *localFakeState) GetAdvertisedDevices() drasriovtypes.AllocatableDevices { return nil }
func (l *localFakeState) UpdatePolicyDevices(_ context.Context, _ map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) error {
	return nil
}

var _ = Describe("matchesNodeSelector", func() {
	var r *SriovResourcePolicyReconciler
	var nodeLabels map[string]string

	BeforeEach(func() {
		r = &SriovResourcePolicyReconciler{}
		nodeLabels = map[string]string{"role": "dpdk", "zone": "a"}
	})

	It("nil selector matches all nodes", func() {
		Expect(r.matchesNodeSelector(nodeLabels, nil)).To(BeTrue())
	})

	It("empty terms matches all nodes", func() {
		Expect(r.matchesNodeSelector(nodeLabels, &corev1.NodeSelector{})).To(BeTrue())
	})

	It("matches when label In expression is satisfied", func() {
		sel := &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      "role",
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"dpdk"},
				}},
			}},
		}
		Expect(r.matchesNodeSelector(nodeLabels, sel)).To(BeTrue())
	})

	It("does not match when label value differs", func() {
		sel := &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      "role",
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"gpu"},
				}},
			}},
		}
		Expect(r.matchesNodeSelector(nodeLabels, sel)).To(BeFalse())
	})

	It("ORs multiple NodeSelectorTerms", func() {
		sel := &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: "role", Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu"},
				}}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"},
				}}},
			},
		}
		Expect(r.matchesNodeSelector(nodeLabels, sel)).To(BeTrue())
	})
})

var _ = Describe("stringSliceContains", func() {
	It("returns expected presence results", func() {
		Expect(stringSliceContains([]string{"a", "b"}, "c")).To(BeFalse())
		Expect(stringSliceContains([]string{"a", "b"}, "b")).To(BeTrue())
	})
})

var _ = Describe("deviceMatchesFilter", func() {
	It("matches valid filters and rejects mismatches", func() {
		r := &SriovResourcePolicyReconciler{}
		vendor := "8086"
		dev := "154c"
		pf := "eth0"
		pci := "0000:00:00.1"
		pcieRoot := "pci0000:00"
		pfPci := "0000:01:00.0"
		d := resourceapi.Device{
			Name: "devA",
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				sriovconsts.AttributeVendorID:      {StringValue: &vendor},
				sriovconsts.AttributeDeviceID:      {StringValue: &dev},
				sriovconsts.AttributeInterfaceType: {StringValue: strPtr("VirtualFunction")},
				sriovconsts.AttributePFName:        {StringValue: &pf},
				sriovconsts.AttributePciAddress:    {StringValue: &pci},
				sriovconsts.AttributePCIeRoot:      {StringValue: &pcieRoot},
				sriovconsts.AttributePfPciAddress:  {StringValue: &pfPci},
			},
		}

		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{})).To(BeTrue())

		f := sriovdrav1alpha1.ResourceFilter{
			Vendors:        []string{"8086"},
			Devices:        []string{"154c"},
			PciAddresses:   []string{"0000:00:00.1"},
			PfNames:        []string{"eth0"},
			PfPciAddresses: []string{"0000:01:00.0"},
		}
		Expect(r.deviceMatchesFilter(d, f)).To(BeTrue())

		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{Vendors: []string{"1234"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{Devices: []string{"9999"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:00:00.2"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:00:00.*"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:00:00.1 "}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PfNames: []string{"eth9"}})).To(BeFalse())
		// Test with a different parent PCI address
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PfPciAddresses: []string{"0000:00:ff.f"}})).To(BeFalse())
	})
})

var _ = Describe("getPolicyDeviceMap", func() {
	It("assigns devices per first-match and supports configs without DeviceAttributesSelector", func() {
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
		r := &SriovResourcePolicyReconciler{deviceStateManager: &localFakeState{alloc: alloc}}

		// No policies -> empty map
		m := r.getPolicyDeviceMap(nil, nil)
		Expect(m).To(BeEmpty())

		// Policy with no DeviceAttributesSelector -- devices are still matched (advertised)
		policies := []*sriovdrav1alpha1.SriovResourcePolicy{{
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{
					{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}}},
				},
			},
		}}
		m = r.getPolicyDeviceMap(policies, nil)
		Expect(m).To(HaveLen(2))
		// No DeviceAttributesSelector -> empty attribute maps
		Expect(m["devA"]).To(BeEmpty())
		Expect(m["devB"]).To(BeEmpty())
	})

	It("resolves DeviceAttributesSelector and applies attributes to matched devices", func() {
		vendor := "8086"
		alloc := drasriovtypes.AllocatableDevices{
			"devA": resourceapi.Device{
				Name: "devA",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID: {StringValue: &vendor},
				},
			},
		}
		r := &SriovResourcePolicyReconciler{deviceStateManager: &localFakeState{alloc: alloc}}

		resName := "my-resource"
		deviceAttrs := []sriovdrav1alpha1.DeviceAttributes{{
			ObjectMeta: metav1.ObjectMeta{Name: "da1", Labels: map[string]string{"pool": "test"}},
			Spec: sriovdrav1alpha1.DeviceAttributesSpec{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					"sriovnetwork.k8snetworkplumbingwg.io/resourceName": {StringValue: &resName},
				},
			},
		}}

		policies := []*sriovdrav1alpha1.SriovResourcePolicy{{
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{
					DeviceAttributesSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"pool": "test"}},
					ResourceFilters:          []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}},
				}},
			},
		}}

		m := r.getPolicyDeviceMap(policies, deviceAttrs)
		Expect(m).To(HaveLen(1))
		Expect(m["devA"]).To(HaveKey(resourceapi.QualifiedName("sriovnetwork.k8snetworkplumbingwg.io/resourceName")))
		Expect(*m["devA"][resourceapi.QualifiedName("sriovnetwork.k8snetworkplumbingwg.io/resourceName")].StringValue).To(Equal("my-resource"))
	})

	It("uses PCI inventory only for configs that request pciAddresses", func() {
		vendor := "8086"
		alloc := drasriovtypes.AllocatableDevices{
			"devA": {
				Name: "devA",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID: {StringValue: &vendor},
				},
			},
		}
		virtualPci := "0000:aa:00.1"
		fakeState := &localFakeState{
			alloc: alloc,
			pciAddressInventory: drasriovtypes.AllocatableDevices{
				"0000-aa-00-1": {
					Name: "0000-aa-00-1",
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						sriovconsts.AttributePciAddress: {StringValue: &virtualPci},
					},
				},
			},
		}
		r := &SriovResourcePolicyReconciler{deviceStateManager: fakeState}

		policies := []*sriovdrav1alpha1.SriovResourcePolicy{{
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{
					{
						ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}},
					},
					{
						ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{PciAddresses: []string{"0000:aa:00.1"}}},
					},
				},
			},
		}}

		matched := r.getPolicyDeviceMap(policies, nil)
		Expect(matched).To(HaveLen(2))
		Expect(matched).To(HaveKey("devA"))
		Expect(matched).To(HaveKey("0000-aa-00-1"))
		Expect(fakeState.includePciCalls).To(Equal([]bool{false, true}))
	})

	It("enforces PF-vs-VF mutual exclusivity when PF selected by pciAddresses", func() {
		vendor := "8086"
		pfPci := "0000:01:00.0"
		pfPciOther := "0000:02:00.0"
		vfPci1 := "0000:01:00.1"
		vfPci2 := "0000:02:00.1"

		alloc := drasriovtypes.AllocatableDevices{
			"0000-01-00-1": {
				Name: "0000-01-00-1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID:     {StringValue: &vendor},
					sriovconsts.AttributePciAddress:   {StringValue: &vfPci1},
					sriovconsts.AttributePfPciAddress: {StringValue: &pfPci},
				},
			},
			"0000-02-00-1": {
				Name: "0000-02-00-1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID:     {StringValue: &vendor},
					sriovconsts.AttributePciAddress:   {StringValue: &vfPci2},
					sriovconsts.AttributePfPciAddress: {StringValue: &pfPciOther},
				},
			},
		}
		fakeState := &localFakeState{
			alloc: alloc,
			pciAddressInventory: drasriovtypes.AllocatableDevices{
				"0000-01-00-0": {
					Name: "0000-01-00-0",
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						sriovconsts.AttributePciAddress:    {StringValue: &pfPci},
						sriovconsts.AttributeInterfaceType: {StringValue: strPtr("Regular")},
					},
				},
			},
		}
		r := &SriovResourcePolicyReconciler{deviceStateManager: fakeState}

		policies := []*sriovdrav1alpha1.SriovResourcePolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "a-vf-policy"},
				Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
					Configs: []sriovdrav1alpha1.Config{
						{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}}},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "z-pf-policy"},
				Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
					Configs: []sriovdrav1alpha1.Config{
						{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{PciAddresses: []string{"0000:01:00.0"}}}},
					},
				},
			},
		}

		matched := r.getPolicyDeviceMap(policies, nil)
		Expect(matched).To(HaveKey("0000-01-00-0"))
		Expect(matched).ToNot(HaveKey("0000-01-00-1"))
		Expect(matched).To(HaveKey("0000-02-00-1"))
	})
})

var _ = Describe("filterRequestsPciAddresses", func() {
	It("returns true only when the filter has pciAddresses", func() {
		Expect(filterRequestsPciAddresses(sriovdrav1alpha1.ResourceFilter{})).To(BeFalse())
		Expect(filterRequestsPciAddresses(sriovdrav1alpha1.ResourceFilter{Vendors: []string{"8086"}})).To(BeFalse())
		Expect(filterRequestsPciAddresses(sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:01:00.1"}})).To(BeTrue())
	})
})

func strPtr(s string) *string {
	return &s
}

type unitFakeState struct {
	alloc               drasriovtypes.AllocatableDevices
	pciAddressInventory drasriovtypes.AllocatableDevices
	includePciCalls     []bool
}

func (l *unitFakeState) GetAllocatableDevices() drasriovtypes.AllocatableDevices { return l.alloc }

func (l *unitFakeState) GetPolicyCandidateDevices(includePciAddressInventory bool) drasriovtypes.AllocatableDevices {
	l.includePciCalls = append(l.includePciCalls, includePciAddressInventory)

	candidates := make(drasriovtypes.AllocatableDevices, len(l.alloc))
	for name, device := range l.alloc {
		candidates[name] = device
	}
	if !includePciAddressInventory {
		return candidates
	}
	for name, device := range l.pciAddressInventory {
		if _, exists := candidates[name]; !exists {
			candidates[name] = device
		}
	}
	return candidates
}

func (l *unitFakeState) UpdatePolicyDevices(_ context.Context, _ map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) error {
	return nil
}

func TestFilterRequestsPciAddresses(t *testing.T) {
	t.Parallel()

	if filterRequestsPciAddresses(sriovdrav1alpha1.ResourceFilter{}) {
		t.Fatalf("expected empty filter to return false")
	}
	if filterRequestsPciAddresses(sriovdrav1alpha1.ResourceFilter{Vendors: []string{"8086"}}) {
		t.Fatalf("expected non-pci filter to return false")
	}
	if !filterRequestsPciAddresses(sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:01:00.1"}}) {
		t.Fatalf("expected pciAddresses filter to return true")
	}
}

func TestGetPolicyDeviceMapUsesPciInventoryOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	vendor := "8086"
	virtualPci := "0000:aa:00.1"
	fakeState := &unitFakeState{
		alloc: drasriovtypes.AllocatableDevices{
			"devA": {
				Name: "devA",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID: {StringValue: &vendor},
				},
			},
		},
		pciAddressInventory: drasriovtypes.AllocatableDevices{
			"0000-aa-00-1": {
				Name: "0000-aa-00-1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributePciAddress: {StringValue: &virtualPci},
				},
			},
		},
	}
	r := &SriovResourcePolicyReconciler{deviceStateManager: fakeState}

	policies := []*sriovdrav1alpha1.SriovResourcePolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
			Configs: []sriovdrav1alpha1.Config{
				{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}}},
				{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{PciAddresses: []string{"0000:aa:00.1"}}}},
			},
		},
	}}

	matched := r.getPolicyDeviceMap(policies, nil)
	if len(matched) != 2 {
		t.Fatalf("expected 2 matched devices, got %d", len(matched))
	}
	if _, ok := matched["devA"]; !ok {
		t.Fatalf("expected allocatable device to match")
	}
	if _, ok := matched["0000-aa-00-1"]; !ok {
		t.Fatalf("expected pci inventory device to match")
	}
	if len(fakeState.includePciCalls) != 2 || fakeState.includePciCalls[0] || !fakeState.includePciCalls[1] {
		t.Fatalf("expected includePci calls [false true], got %v", fakeState.includePciCalls)
	}
}

func TestGetPolicyDeviceMapDoesNotBroadenNonPciFilterToInventory(t *testing.T) {
	t.Parallel()

	vendor := "8086"
	virtualPci := "0000:aa:00.1"
	extraInventoryPci := "0000:aa:00.2"
	fakeState := &unitFakeState{
		alloc: drasriovtypes.AllocatableDevices{
			"devA": {
				Name: "devA",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID: {StringValue: &vendor},
				},
			},
		},
		pciAddressInventory: drasriovtypes.AllocatableDevices{
			"0000-aa-00-1": {
				Name: "0000-aa-00-1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributePciAddress: {StringValue: &virtualPci},
					sriovconsts.AttributeVendorID:   {StringValue: &vendor},
				},
			},
			"0000-aa-00-2": {
				Name: "0000-aa-00-2",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributePciAddress: {StringValue: &extraInventoryPci},
					sriovconsts.AttributeVendorID:   {StringValue: &vendor},
				},
			},
		},
	}
	r := &SriovResourcePolicyReconciler{deviceStateManager: fakeState}

	policies := []*sriovdrav1alpha1.SriovResourcePolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
			Configs: []sriovdrav1alpha1.Config{
				{
					ResourceFilters: []sriovdrav1alpha1.ResourceFilter{
						{PciAddresses: []string{"0000:aa:00.1"}},
						{Vendors: []string{"8086"}},
					},
				},
			},
		},
	}}

	matched := r.getPolicyDeviceMap(policies, nil)
	if len(matched) != 2 {
		t.Fatalf("expected exactly 2 matched devices, got %d", len(matched))
	}
	if _, ok := matched["0000-aa-00-2"]; ok {
		t.Fatalf("unexpected inventory-only device matched via non-pci filter")
	}
}

func TestGetPolicyDeviceMapPfSelectionPreventsChildVfSelection(t *testing.T) {
	t.Parallel()

	vendor := "8086"
	pfPci := "0000:01:00.0"
	vfPci := "0000:01:00.1"

	fakeState := &unitFakeState{
		alloc: drasriovtypes.AllocatableDevices{
			"0000-01-00-1": {
				Name: "0000-01-00-1",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributeVendorID:     {StringValue: &vendor},
					sriovconsts.AttributePciAddress:   {StringValue: &vfPci},
					sriovconsts.AttributePfPciAddress: {StringValue: &pfPci},
				},
			},
		},
		pciAddressInventory: drasriovtypes.AllocatableDevices{
			"0000-01-00-0": {
				Name: "0000-01-00-0",
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					sriovconsts.AttributePciAddress:    {StringValue: &pfPci},
					sriovconsts.AttributeInterfaceType: {StringValue: stringPtr(sriovconsts.InterfaceTypeRegular)},
				},
			},
		},
	}
	r := &SriovResourcePolicyReconciler{deviceStateManager: fakeState}

	policies := []*sriovdrav1alpha1.SriovResourcePolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "a-vf"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}}}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "z-pf"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{PciAddresses: []string{"0000:01:00.0"}}}}},
			},
		},
	}

	matched := r.getPolicyDeviceMap(policies, nil)
	if _, ok := matched["0000-01-00-1"]; ok {
		t.Fatalf("expected VF to be excluded when parent PF is selected by pciAddresses")
	}
	if _, ok := matched["0000-01-00-0"]; !ok {
		t.Fatalf("expected PF to be selected")
	}
}

func stringPtr(s string) *string {
	return &s
}
