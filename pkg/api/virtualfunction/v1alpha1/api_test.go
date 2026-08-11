/*
 * Copyright 2023 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

func testPtr[T any](value T) *T {
	p := new(T)
	*p = value
	return p
}

var _ = Describe("VfConfig", func() {
	Describe("DefaultVfConfig", func() {
		It("should return a valid default config", func() {
			config := DefaultVfConfig()
			Expect(config).NotTo(BeNil())
		})

		It("should set correct TypeMeta", func() {
			config := DefaultVfConfig()
			Expect(config.APIVersion).To(Equal(consts.GroupName + "/" + Version))
			Expect(config.Kind).To(Equal(VfConfigKind))
		})

		It("should have empty string defaults for all fields", func() {
			config := DefaultVfConfig()
			Expect(config.Driver).To(Equal(""))
			Expect(config.IfName).To(Equal(""))
			Expect(config.NetAttachDefName).To(Equal(""))
		})

		It("should have AddVhostMount set to false by default", func() {
			config := DefaultVfConfig()
			Expect(config.AddVhostMount).To(BeFalse())
		})
	})

	Describe("Validate", func() {
		Context("Success Cases", func() {
			It("should validate config with all required fields", func() {
				config := &VfConfig{
					Driver:           "vfio-pci",
					NetAttachDefName: "test-network",
				}
				err := config.Validate()
				Expect(err).NotTo(HaveOccurred())
			})

			It("should validate config with additional optional fields", func() {
				config := &VfConfig{
					Driver:                "netdevice",
					NetAttachDefName:      "test-network",
					IfName:                "eth0",
					AddVhostMount:         true,
					NetAttachDefNamespace: "default",
				}
				err := config.Validate()
				Expect(err).NotTo(HaveOccurred())
			})

			It("should validate config with minimal required fields", func() {
				config := &VfConfig{
					Driver:           "vfio-pci",
					NetAttachDefName: "net",
				}
				err := config.Validate()
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Error Cases", func() {
			It("should return error when Driver is empty", func() {
				config := &VfConfig{
					Driver:           "",
					NetAttachDefName: "test-network",
				}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no driver set"))
			})

			It("should return error when NetAttachDefName is empty", func() {
				config := &VfConfig{
					Driver:           "vfio-pci",
					NetAttachDefName: "",
				}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no net attach def name set"))
			})

			It("should return error when both Driver and NetAttachDefName are empty", func() {
				config := &VfConfig{
					Driver:           "",
					NetAttachDefName: "",
				}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no driver set"))
			})

			It("should return error for default config without modifications", func() {
				config := DefaultVfConfig()
				err := config.Validate()
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("Override", func() {
		Context("Override All Fields", func() {
			It("should override all fields when other has all fields set", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					Driver:           "netdevice",
					IfName:           "eth1",
					NetAttachDefName: "net2",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("netdevice"))
				Expect(base.IfName).To(Equal("eth1"))
				Expect(base.NetAttachDefName).To(Equal("net2"))
			})
		})

		Context("Partial Override", func() {
			It("should override only Driver when other only has Driver set", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					Driver: "netdevice",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("netdevice"))
				Expect(base.IfName).To(Equal("eth0"))
				Expect(base.NetAttachDefName).To(Equal("net1"))
			})

			It("should override only IfName when other only has IfName set", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					IfName: "eth1",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("vfio-pci"))
				Expect(base.IfName).To(Equal("eth1"))
				Expect(base.NetAttachDefName).To(Equal("net1"))
			})

			It("should override only NetAttachDefName when other only has NetAttachDefName set", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					NetAttachDefName: "net2",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("vfio-pci"))
				Expect(base.IfName).To(Equal("eth0"))
				Expect(base.NetAttachDefName).To(Equal("net2"))
			})

			It("should override multiple fields but not all", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					Driver:           "netdevice",
					NetAttachDefName: "net2",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("netdevice"))
				Expect(base.IfName).To(Equal("eth0"))
				Expect(base.NetAttachDefName).To(Equal("net2"))
			})
		})

		Context("Empty String Behavior", func() {
			It("should not override when other has empty strings", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					Driver:           "",
					IfName:           "",
					NetAttachDefName: "",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("vfio-pci"))
				Expect(base.IfName).To(Equal("eth0"))
				Expect(base.NetAttachDefName).To(Equal("net1"))
			})

			It("should preserve base empty values when other is also empty", func() {
				base := &VfConfig{
					Driver:           "",
					IfName:           "",
					NetAttachDefName: "",
				}
				other := &VfConfig{
					Driver:           "",
					IfName:           "",
					NetAttachDefName: "",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal(""))
				Expect(base.IfName).To(Equal(""))
				Expect(base.NetAttachDefName).To(Equal(""))
			})

			It("should override empty base with non-empty other", func() {
				base := &VfConfig{
					Driver:           "",
					IfName:           "",
					NetAttachDefName: "",
				}
				other := &VfConfig{
					Driver:           "vfio-pci",
					IfName:           "eth0",
					NetAttachDefName: "net1",
				}

				base.Override(other)

				Expect(base.Driver).To(Equal("vfio-pci"))
				Expect(base.IfName).To(Equal("eth0"))
				Expect(base.NetAttachDefName).To(Equal("net1"))
			})
		})

		Context("Multiple Overrides", func() {
			It("should correctly handle sequential overrides", func() {
				base := DefaultVfConfig()

				override1 := &VfConfig{
					Driver: "vfio-pci",
				}
				base.Override(override1)
				Expect(base.Driver).To(Equal("vfio-pci"))

				override2 := &VfConfig{
					NetAttachDefName: "net1",
				}
				base.Override(override2)
				Expect(base.Driver).To(Equal("vfio-pci"))
				Expect(base.NetAttachDefName).To(Equal("net1"))

				override3 := &VfConfig{
					IfName: "eth0",
					Driver: "netdevice",
				}
				base.Override(override3)
				Expect(base.Driver).To(Equal("netdevice"))
				Expect(base.NetAttachDefName).To(Equal("net1"))
				Expect(base.IfName).To(Equal("eth0"))
			})
		})

		Context("Fields Not Affected by Override", func() {
			It("should not affect TypeMeta fields", func() {
				base := &VfConfig{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "original/v1",
						Kind:       "OriginalKind",
					},
					Driver:           "vfio-pci",
					NetAttachDefName: "net1",
				}
				other := &VfConfig{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "other/v1",
						Kind:       "OtherKind",
					},
					Driver:           "netdevice",
					NetAttachDefName: "net2",
				}

				base.Override(other)

				// TypeMeta should remain unchanged
				Expect(base.APIVersion).To(Equal("original/v1"))
				Expect(base.Kind).To(Equal("OriginalKind"))
				// But regular fields should be overridden
				Expect(base.Driver).To(Equal("netdevice"))
				Expect(base.NetAttachDefName).To(Equal("net2"))
			})

			It("should not affect AddVhostMount field", func() {
				base := &VfConfig{
					Driver:           "vfio-pci",
					NetAttachDefName: "net1",
					AddVhostMount:    true,
				}
				other := &VfConfig{
					Driver:           "netdevice",
					NetAttachDefName: "net2",
					AddVhostMount:    false,
				}

				base.Override(other)

				// AddVhostMount should remain unchanged as it's not in Override logic
				Expect(base.AddVhostMount).To(BeTrue())
				Expect(base.Driver).To(Equal("netdevice"))
			})

			It("should not affect NetAttachDefNamespace field", func() {
				base := &VfConfig{
					Driver:                "vfio-pci",
					NetAttachDefName:      "net1",
					NetAttachDefNamespace: "default",
				}
				other := &VfConfig{
					Driver:                "netdevice",
					NetAttachDefName:      "net2",
					NetAttachDefNamespace: "other-namespace",
				}

				base.Override(other)

				// NetAttachDefNamespace should remain unchanged
				Expect(base.NetAttachDefNamespace).To(Equal("default"))
				Expect(base.Driver).To(Equal("netdevice"))
			})
		})
	})

	Describe("Normalize", func() {
		It("should not panic when called", func() {
			config := &VfConfig{
				Driver:           "vfio-pci",
				NetAttachDefName: "test-net",
			}
			Expect(func() { config.Normalize() }).NotTo(Panic())
		})

		It("should not modify config fields", func() {
			config := &VfConfig{
				Driver:           "vfio-pci",
				IfName:           "eth0",
				NetAttachDefName: "test-net",
			}
			config.Normalize()

			Expect(config.Driver).To(Equal("vfio-pci"))
			Expect(config.IfName).To(Equal("eth0"))
			Expect(config.NetAttachDefName).To(Equal("test-net"))
		})

		It("should work with default config", func() {
			config := DefaultVfConfig()
			Expect(func() { config.Normalize() }).NotTo(Panic())
		})
	})

	Describe("VF link attributes", func() {
		Context("Validate", func() {
			It("should accept a config with valid VF attributes", func() {
				config := &VfConfig{
					Driver:           "netdevice",
					NetAttachDefName: "net",
					VF: &VFLinkConfig{
						VLAN:      testPtr(100),
						Qos:       testPtr(3),
						VlanProto: testPtr(VlanProto8021ad),
						SpoofChk:  testPtr(true),
						Trust:     testPtr(false),
						MinTxRate: testPtr(100),
						MaxTxRate: testPtr(1000),
						LinkState: testPtr(LinkStateEnable),
					},
				}
				Expect(config.Validate()).NotTo(HaveOccurred())
			})

			It("should accept a nil VF block", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net"}
				Expect(config.Validate()).NotTo(HaveOccurred())
			})

			It("should reject an out-of-range vlan", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net", VF: &VFLinkConfig{VLAN: testPtr(5000)}}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("vlan"))
			})

			It("should reject an out-of-range qos", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net", VF: &VFLinkConfig{Qos: testPtr(9)}}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("qos"))
			})

			It("should reject an invalid vlanProto", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net", VF: &VFLinkConfig{VlanProto: testPtr("802.1x")}}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("vlanProto"))
			})

			It("should reject an invalid linkState", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net", VF: &VFLinkConfig{LinkState: testPtr("up")}}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("linkState"))
			})

			It("should reject minTxRate greater than maxTxRate", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net", VF: &VFLinkConfig{MinTxRate: testPtr(2000), MaxTxRate: testPtr(1000)}}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("maxTxRate"))
			})

			It("should reject a negative txRate", func() {
				config := &VfConfig{Driver: "netdevice", NetAttachDefName: "net", VF: &VFLinkConfig{MaxTxRate: testPtr(-1)}}
				err := config.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("maxTxRate"))
			})
		})

		Context("Override", func() {
			It("should set the VF block when base has none", func() {
				base := &VfConfig{Driver: "netdevice", NetAttachDefName: "net"}
				other := &VfConfig{VF: &VFLinkConfig{VLAN: testPtr(100), Trust: testPtr(true)}}
				base.Override(other)
				Expect(base.VF).NotTo(BeNil())
				Expect(*base.VF.VLAN).To(Equal(100))
				Expect(*base.VF.Trust).To(BeTrue())
			})

			It("should merge only non-nil VF fields", func() {
				base := &VfConfig{VF: &VFLinkConfig{VLAN: testPtr(100), Qos: testPtr(2), Trust: testPtr(false)}}
				other := &VfConfig{VF: &VFLinkConfig{VLAN: testPtr(200), LinkState: testPtr(LinkStateAuto)}}
				base.Override(other)
				Expect(*base.VF.VLAN).To(Equal(200))                // overridden
				Expect(*base.VF.Qos).To(Equal(2))                   // preserved
				Expect(*base.VF.Trust).To(BeFalse())                // preserved
				Expect(*base.VF.LinkState).To(Equal(LinkStateAuto)) // added
			})

			It("should leave the VF block untouched when other has none", func() {
				base := &VfConfig{VF: &VFLinkConfig{VLAN: testPtr(100)}}
				other := &VfConfig{Driver: "netdevice"}
				base.Override(other)
				Expect(base.VF).NotTo(BeNil())
				Expect(*base.VF.VLAN).To(Equal(100))
			})
		})
	})
})
