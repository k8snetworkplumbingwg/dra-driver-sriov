package controller

import (
	"context"

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
	alloc drasriovtypes.AllocatableDevices
}

func (l *localFakeState) GetAllocatableDevices() drasriovtypes.AllocatableDevices { return l.alloc }
func (l *localFakeState) GetAdvertisedDevices() drasriovtypes.AllocatableDevices  { return nil }
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
		linkType := "ethernet"
		d := resourceapi.Device{
			Name: "devA",
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				sriovconsts.AttributeVendorID:     {StringValue: &vendor},
				sriovconsts.AttributeDeviceID:     {StringValue: &dev},
				sriovconsts.AttributePFName:       {StringValue: &pf},
				sriovconsts.AttributePciAddress:   {StringValue: &pci},
				sriovconsts.AttributePCIeRoot:     {StringValue: &pcieRoot},
				sriovconsts.AttributePfPciAddress: {StringValue: &pfPci},
				sriovconsts.AttributeLinkType:     {StringValue: &linkType},
			},
		}

		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{})).To(BeTrue())

		f := sriovdrav1alpha1.ResourceFilter{
			Vendors:        []string{"8086"},
			Devices:        []string{"154c"},
			PciAddresses:   []string{"0000:00:00.1"},
			PfNames:        []string{"eth0"},
			PfPciAddresses: []string{"0000:01:00.0"},
			LinkType:       sriovconsts.LinkTypeEthernet,
		}
		Expect(r.deviceMatchesFilter(d, f)).To(BeTrue())

		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{Vendors: []string{"1234"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{Devices: []string{"9999"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PciAddresses: []string{"0000:00:00.2"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PfNames: []string{"eth9"}})).To(BeFalse())
		// Test with a different parent PCI address
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{PfPciAddresses: []string{"0000:00:ff.f"}})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{LinkType: sriovconsts.LinkTypeInfiniband})).To(BeFalse())

		// Multiple filters set: all must match
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{
			Vendors:  []string{"8086"},
			LinkType: sriovconsts.LinkTypeEthernet,
		})).To(BeTrue())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{
			Vendors:  []string{"8086"},
			LinkType: sriovconsts.LinkTypeInfiniband,
		})).To(BeFalse())
		Expect(r.deviceMatchesFilter(d, sriovdrav1alpha1.ResourceFilter{
			Vendors:  []string{"1234"},
			LinkType: sriovconsts.LinkTypeEthernet,
		})).To(BeFalse())
	})
})

var _ = Describe("deviceMatchesFilters linkType", func() {
	var r *SriovResourcePolicyReconciler

	BeforeEach(func() {
		r = &SriovResourcePolicyReconciler{}
	})

	makeDevice := func(lt string) resourceapi.Device {
		return resourceapi.Device{
			Name: "dev-lt",
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				sriovconsts.AttributeLinkType: {StringValue: &lt},
			},
		}
	}

	filters := func(lt string) []sriovdrav1alpha1.ResourceFilter {
		return []sriovdrav1alpha1.ResourceFilter{{LinkType: lt}}
	}

	It("matches ethernet device with 'eth' (lowercase)", func() {
		d := makeDevice(sriovconsts.LinkTypeEthernet)
		Expect(r.deviceMatchesFilters(d, filters("eth"))).To(BeTrue())
	})

	It("matches ethernet device with 'ETH' (uppercase)", func() {
		d := makeDevice(sriovconsts.LinkTypeEthernet)
		Expect(r.deviceMatchesFilters(d, filters("ETH"))).To(BeTrue())
	})

	It("matches ethernet device with 'Eth' (mixed case)", func() {
		d := makeDevice(sriovconsts.LinkTypeEthernet)
		Expect(r.deviceMatchesFilters(d, filters("Eth"))).To(BeTrue())
	})

	It("matches infiniband device with 'ib' (lowercase)", func() {
		d := makeDevice(sriovconsts.LinkTypeInfiniband)
		Expect(r.deviceMatchesFilters(d, filters("ib"))).To(BeTrue())
	})

	It("matches infiniband device with 'IB' (uppercase)", func() {
		d := makeDevice(sriovconsts.LinkTypeInfiniband)
		Expect(r.deviceMatchesFilters(d, filters("IB"))).To(BeTrue())
	})

	It("does not match ethernet device with 'ib' filter", func() {
		d := makeDevice(sriovconsts.LinkTypeEthernet)
		Expect(r.deviceMatchesFilters(d, filters("ib"))).To(BeFalse())
	})

	It("does not match infiniband device with 'eth' filter", func() {
		d := makeDevice(sriovconsts.LinkTypeInfiniband)
		Expect(r.deviceMatchesFilters(d, filters("eth"))).To(BeFalse())
	})

	It("does not match when device has no linkType attribute", func() {
		d := resourceapi.Device{
			Name:       "dev-no-lt",
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{},
		}
		Expect(r.deviceMatchesFilters(d, filters("eth"))).To(BeFalse())
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
})
