package devicestate

import (
	"bytes"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

// encodeVfConfig encodes a VfConfig fixture and fails the spec on error.
func encodeVfConfig(vf *configapi.VfConfig) []byte {
	raw, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vf)
	Expect(err).NotTo(HaveOccurred())
	return raw
}

// classAllocConfig wraps a copied config in a FromClass allocation entry.
func classAllocConfig(requests []string, vf *configapi.VfConfig) resourceapi.DeviceAllocationConfiguration {
	cfg := vf.DeepCopy()
	cfg.APIVersion = configapi.GroupName + "/v1alpha1"
	cfg.Kind = configapi.VfConfigKind
	raw, _ := json.Marshal(cfg)
	return resourceapi.DeviceAllocationConfiguration{Source: resourceapi.AllocationConfigSourceClass, Requests: requests, DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: consts.DriverName, Parameters: runtime.RawExtension{Raw: raw}}}}
}

var _ = Describe("ResolveClaimConfiguration", func() {
	It("accepts class config with all VfConfig fields preserved", func() {
		vf := &configapi.VfConfig{
			TypeMeta:              metav1.TypeMeta{APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1", Kind: "VfConfig"},
			Driver:                "vfio-pci",
			AddVhostMount:         true,
			IfName:                "net-blue",
			NetAttachDefName:      "blue-net",
			NetAttachDefNamespace: "sriov-system",
		}
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1", UID: "uid-1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							classAllocConfig([]string{"vf"}, vf),
						},
					},
				},
			},
		}

		addTestRequests(claim)
		resolved, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).NotTo(BeNil())
		Expect(resolved.Fingerprint).NotTo(BeEmpty())
		Expect(resolved.ByKey).To(HaveLen(1))
		for _, entry := range resolved.ByKey {
			Expect(entry.Config.Driver).To(Equal("vfio-pci"))
			Expect(entry.Config.AddVhostMount).To(BeTrue())
			Expect(entry.Config.IfName).To(Equal("net-blue"))
			Expect(entry.Config.NetAttachDefName).To(Equal("blue-net"))
			Expect(entry.Config.NetAttachDefNamespace).To(Equal("sriov-system"))
		}
	})

	It("rejects claim spec opaque config for this driver", func() {
		vf := &configapi.VfConfig{Driver: "vfio-pci"}
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Spec: resourceapi.ResourceClaimSpec{
				Devices: resourceapi.DeviceClaim{
					Config: []resourceapi.DeviceClaimConfiguration{
						{
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver: consts.DriverName,
									Parameters: runtime.RawExtension{
										Raw: encodeVfConfig(vf),
									},
								},
							},
						},
					},
				},
			},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
					},
				},
			},
		}

		addTestRequests(claim)
		_, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not allowed in claim spec"))
	})

	It("rejects allocation config FromClaim", func() {
		vf := &configapi.VfConfig{Driver: "vfio-pci"}
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							{
								Source:   resourceapi.AllocationConfigSourceClaim,
								Requests: []string{"vf"},
								DeviceConfiguration: resourceapi.DeviceConfiguration{
									Opaque: &resourceapi.OpaqueDeviceConfiguration{
										Driver:     consts.DriverName,
										Parameters: runtime.RawExtension{Raw: encodeVfConfig(vf)},
									},
								},
							},
						},
					},
				},
			},
		}

		addTestRequests(claim)
		_, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("FromClaim"))
		Expect(err.Error()).To(ContainSubstring("requests [vf]"))
		Expect(err.Error()).To(ContainSubstring("ns1/c1"))
	})

	It("rejects class-sourced allocation config without opaque parameters", func() {
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							{Source: resourceapi.AllocationConfigSourceClass, Requests: []string{"vf"}},
						},
					},
				},
			},
		}

		addTestRequests(claim)
		_, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("requires opaque parameters"))
	})

	It("validates class config for unselected request scopes", func() {
		vf := &configapi.VfConfig{
			TypeMeta:         metav1.TypeMeta{APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1", Kind: "VfConfig"},
			NetAttachDefName: "only-name",
		}
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							classAllocConfig([]string{"other-req"}, vf),
						},
					},
				},
			},
		}

		addTestRequests(claim)
		_, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeStandalone))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("class configuration"))
		Expect(err.Error()).To(ContainSubstring("FromClass"))
		Expect(err.Error()).To(ContainSubstring("other-req"))
	})

	It("rejects overlapping class entries", func() {
		vf1 := &configapi.VfConfig{
			TypeMeta: metav1.TypeMeta{APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1", Kind: "VfConfig"},
			Driver:   "vfio-pci",
		}
		vf2 := &configapi.VfConfig{
			TypeMeta: metav1.TypeMeta{APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1", Kind: "VfConfig"},
			IfName:   "net1",
		}
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							classAllocConfig([]string{"vf"}, vf1),
							classAllocConfig([]string{"vf"}, vf2),
						},
					},
				},
			},
		}

		addTestRequests(claim)
		_, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("overlapping"))
	})

	It("ignores foreign-driver opaque allocation config", func() {
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1", UID: "uid-1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							{
								Source: resourceapi.AllocationConfigSourceClass,
								DeviceConfiguration: resourceapi.DeviceConfiguration{
									Opaque: &resourceapi.OpaqueDeviceConfiguration{
										Driver: "gpu.example.com",
										Parameters: runtime.RawExtension{
											Raw: []byte(`{"kind":"Other"}`),
										},
									},
								},
							},
						},
					},
				},
			},
		}

		// Ignore foreign configuration before validating its source or payload.
		foreign := &claim.Status.Allocation.Devices.Config[0]
		foreign.Source = resourceapi.AllocationConfigSource("unknown")
		claim.Spec.Devices.Config = []resourceapi.DeviceClaimConfiguration{{DeviceConfiguration: foreign.DeviceConfiguration}}
		claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results,
			resourceapi.DeviceRequestAllocationResult{Driver: "gpu.example.com", Request: "gpu", Device: "gpu1", Pool: "gpu-pool"})
		addTestRequests(claim)
		resolved, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.ByKey).To(HaveLen(1))
		Expect(resolved.ByKey[allocationKeyFromResult(&claim.Status.Allocation.Devices.Results[0])].Config.Driver).To(BeEmpty())
	})

	It("applies parent-scoped class config to subrequest results", func() {
		vf := &configapi.VfConfig{
			TypeMeta: metav1.TypeMeta{APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1", Kind: "VfConfig"},
			IfName:   "net-parent",
		}
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1", UID: "uid-1"},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Driver: consts.DriverName, Device: "dev1", Request: "network/vf0", Pool: "pool1"},
						},
						Config: []resourceapi.DeviceAllocationConfiguration{
							classAllocConfig([]string{"network"}, vf),
						},
					},
				},
			},
		}

		addTestRequests(claim)
		resolved, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
		Expect(err).NotTo(HaveOccurred())
		key := allocationKeyFromResult(&claim.Status.Allocation.Devices.Results[0])
		Expect(resolved.ByKey[key].Config.IfName).To(Equal("net-parent"))
	})

	Context("allocation snapshot boundaries", func() {
		var claim *resourceapi.ResourceClaim
		BeforeEach(func() {
			claim = &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "c1", UID: "uid-1"},
				Spec: resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{
					{Name: "vf", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "blue"}},
				}}},
				Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{{Request: "vf", Driver: consts.DriverName, Pool: "node-a", Device: "dev1"}},
					Config: []resourceapi.DeviceAllocationConfiguration{classAllocConfig(nil, &configapi.VfConfig{
						Driver: "vfio-pci", NetAttachDefName: "blue-net", NetAttachDefNamespace: "admin",
					})},
				}}},
			}
		})

		It("keeps fingerprints stable across result ordering, JSON formatting and reported status", func() {
			second := claim.Status.Allocation.Devices.Results[0]
			second.Device = "dev2"
			claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results, second)
			before, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			claim.Status.Allocation.Devices.Results[0], claim.Status.Allocation.Devices.Results[1] = claim.Status.Allocation.Devices.Results[1], claim.Status.Allocation.Devices.Results[0]
			var pretty bytes.Buffer
			Expect(json.Indent(&pretty, claim.Status.Allocation.Devices.Config[0].Opaque.Parameters.Raw, "", "  ")).To(Succeed())
			claim.Status.Allocation.Devices.Config[0].Opaque.Parameters.Raw = pretty.Bytes()
			claim.ResourceVersion = "42"
			claim.Status.Devices = []resourceapi.AllocatedDeviceStatus{{NetworkData: &resourceapi.NetworkDeviceData{InterfaceName: "vfnet7"}}}
			after, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Fingerprint).To(Equal(before.Fingerprint))
			entries := claim.Status.Allocation.Devices.Results
			firstKey, secondKey := allocationKeyFromResult(&entries[0]), allocationKeyFromResult(&entries[1])
			after.ByKey[firstKey].Config.Driver = "default"
			Expect(after.ByKey[secondKey].Config.Driver).To(Equal("vfio-pci"), "per-device configs must be independent")
			Expect(before.ByKey[firstKey].Config.Driver).To(Equal("vfio-pci"))
		})

		It("fingerprints the normalized configuration mode", func() {
			standalone, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeStandalone))
			Expect(err).NotTo(HaveOccurred())
			defaultMode, err := ResolveClaimConfiguration(claim, configapi.Decoder, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(defaultMode.ConfigurationMode).To(Equal(string(consts.ConfigurationModeStandalone)))
			Expect(defaultMode.Fingerprint).To(Equal(standalone.Fingerprint))
			multus, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			Expect(multus.Fingerprint).NotTo(Equal(standalone.Fingerprint))
		})

		DescribeTable("fingerprints each trusted VfConfig field", func(change func(*configapi.VfConfig)) {
			before, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			cfg := before.ByKey[allocationKeyFromResult(&claim.Status.Allocation.Devices.Results[0])].Config.DeepCopy()
			change(cfg)
			claim.Status.Allocation.Devices.Config[0] = classAllocConfig(nil, cfg)
			after, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Fingerprint).NotTo(Equal(before.Fingerprint))
		},
			Entry("driver", func(c *configapi.VfConfig) { c.Driver = "default" }),
			Entry("vhost", func(c *configapi.VfConfig) { c.AddVhostMount = true }),
			Entry("fixed interface name", func(c *configapi.VfConfig) { c.IfName = "net-blue" }),
			Entry("NAD name", func(c *configapi.VfConfig) { c.NetAttachDefName = "red-net" }),
			Entry("NAD namespace", func(c *configapi.VfConfig) { c.NetAttachDefNamespace = "other-admin" }),
		)

		DescribeTable("fingerprints allocation and class identity", func(change func(*resourceapi.ResourceClaim)) {
			before, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			change(claim)
			after, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Fingerprint).NotTo(Equal(before.Fingerprint))
		},
			Entry("claim UID", func(c *resourceapi.ResourceClaim) { c.UID = "replacement" }),
			Entry("class", func(c *resourceapi.ResourceClaim) { c.Spec.Devices.Requests[0].Exactly.DeviceClassName = "red" }),
			Entry("pool", func(c *resourceapi.ResourceClaim) { c.Status.Allocation.Devices.Results[0].Pool = "node-b" }),
			Entry("device", func(c *resourceapi.ResourceClaim) { c.Status.Allocation.Devices.Results[0].Device = "dev2" }),
			Entry("share", func(c *resourceapi.ResourceClaim) {
				id := k8stypes.UID("share-1")
				c.Status.Allocation.Devices.Results[0].ShareID = &id
			}),
		)

		DescribeTable("rejects untrusted allocation sources", func(source resourceapi.AllocationConfigSource) {
			claim.Status.Allocation.Devices.Config[0].Source = source
			claim.Status.Allocation.Devices.Config[0].Requests = []string{"vf"}
			resolved, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(resolved).To(BeNil())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requests [vf]"))
		}, Entry("empty", resourceapi.AllocationConfigSource("")), Entry("unknown", resourceapi.AllocationConfigSource("unknown")))

		It("rejects malformed payloads without exposing their contents", func() {
			claim.Status.Allocation.Devices.Config[0].Opaque.Parameters.Raw = []byte(`{"apiVersion":"invalid","kind":"VfConfig","secret":"do-not-log"}`)
			resolved, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(resolved).To(BeNil())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FromClass"))
			Expect(err.Error()).NotTo(ContainSubstring("do-not-log"))
		})

		It("isolates the selected subrequest and rejects multiple selected siblings", func() {
			claim.Spec.Devices.Requests[0].Exactly = nil
			claim.Spec.Devices.Requests[0].FirstAvailable = []resourceapi.DeviceSubRequest{{Name: "blue", DeviceClassName: "blue"}, {Name: "red", DeviceClassName: "red"}}
			claim.Status.Allocation.Devices.Results[0].Request = "vf/blue"
			claim.Status.Allocation.Devices.Config[0].Requests = []string{"vf/blue"}
			claim.Status.Allocation.Devices.Config = append(claim.Status.Allocation.Devices.Config, classAllocConfig([]string{"vf/red"}, &configapi.VfConfig{Driver: "default"}))
			resolved, err := ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).NotTo(HaveOccurred())
			entry := resolved.ByKey[allocationKeyFromResult(&claim.Status.Allocation.Devices.Results[0])]
			Expect(entry.ClassName).To(Equal("blue"))
			Expect(entry.Config.Driver).To(Equal("vfio-pci"))
			claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results, resourceapi.DeviceRequestAllocationResult{Request: "vf/red", Driver: consts.DriverName, Pool: "node-a", Device: "dev2"})
			_, err = ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeMultus))
			Expect(err).To(MatchError(ContainSubstring("multiple firstAvailable subrequests")))
		})
	})
})

// testResolvedFromRequestConfigs builds device-state fixtures without production source-policy checks.
func testResolvedFromRequestConfigs(claim *resourceapi.ResourceClaim, configs map[string]*configapi.VfConfig, mode string) *ResolvedClaimConfiguration {
	byKey := make(map[AllocationKey]ResolvedDeviceConfiguration)
	for i := range claim.Status.Allocation.Devices.Results {
		result := &claim.Status.Allocation.Devices.Results[i]
		if result.Driver != consts.DriverName {
			continue
		}
		cfg := configs[result.Request]
		if cfg == nil {
			cfg = configapi.DefaultVfConfig()
		}
		cfg = cfg.DeepCopy()
		Expect(configapi.ValidateForPrepare(mode, cfg)).NotTo(HaveOccurred())
		key := allocationKeyFromResult(result)
		byKey[key] = ResolvedDeviceConfiguration{
			Key:         key,
			RequestName: result.Request,
			Config:      cfg.DeepCopy(),
		}
	}
	resolved := &ResolvedClaimConfiguration{
		ClaimUID:            claim.UID,
		ConfigPolicyVersion: consts.DeviceClassAdminConfigPolicyVersion,
		ByKey:               byKey,
	}
	var err error
	resolved.Fingerprint, err = fingerprintClaimConfiguration(resolved)
	Expect(err).NotTo(HaveOccurred())
	return resolved
}
