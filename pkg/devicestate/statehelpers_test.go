package devicestate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

var _ = Describe("getMapOfOpaqueDeviceConfigForDevice", func() {
	var decoder runtime.Decoder

	BeforeEach(func() {
		decoder = configapi.Decoder
	})

	Context("Success Cases", func() {
		It("should process single class config", func() {
			vfConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "test-net",
			}
			encoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vfConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result["request1"]).NotTo(BeNil())
			Expect(result["request1"].Driver).To(Equal("vfio-pci"))
			Expect(result["request1"].NetAttachDefName).To(Equal("test-net"))
		})

		It("should process single claim config", func() {
			vfConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "netdevice",
				NetAttachDefName: "claim-net",
			}
			encoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vfConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClaim,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result["request1"].Driver).To(Equal("netdevice"))
		})

		It("should handle multiple requests in single config", func() {
			vfConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "shared-net",
			}
			encoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vfConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1", "request2", "request3"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(3))
			Expect(result["request1"].NetAttachDefName).To(Equal("shared-net"))
			Expect(result["request2"].NetAttachDefName).To(Equal("shared-net"))
			Expect(result["request3"].NetAttachDefName).To(Equal("shared-net"))
		})
	})

	Context("Config Precedence", func() {
		It("should apply claim config over class config", func() {
			classConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "class-net",
				IfName:           "eth0",
			}
			claimConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "netdevice",
				NetAttachDefName: "claim-net",
			}

			classEncoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), classConfig)
			Expect(err).NotTo(HaveOccurred())
			claimEncoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), claimConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: classEncoded,
							},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClaim,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: claimEncoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			// Claim config should override class config
			Expect(result["request1"].Driver).To(Equal("netdevice"))
			Expect(result["request1"].NetAttachDefName).To(Equal("claim-net"))
			// Non-overridden field from class should remain
			Expect(result["request1"].IfName).To(Equal("eth0"))
		})

		It("should apply later config over earlier config within same source", func() {
			config1 := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "net1",
			}
			config2 := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver: "netdevice",
			}

			encoded1, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), config1)
			Expect(err).NotTo(HaveOccurred())
			encoded2, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), config2)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded1,
							},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded2,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			// Later config overrides driver
			Expect(result["request1"].Driver).To(Equal("netdevice"))
			// But NetAttachDefName from earlier config remains
			Expect(result["request1"].NetAttachDefName).To(Equal("net1"))
		})

		It("should handle partial overrides correctly", func() {
			baseConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "base-net",
				IfName:           "eth0",
			}
			overrideConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				NetAttachDefName: "override-net",
			}

			baseEncoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), baseConfig)
			Expect(err).NotTo(HaveOccurred())
			overrideEncoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), overrideConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: baseEncoded,
							},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClaim,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: overrideEncoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			// Overridden field
			Expect(result["request1"].NetAttachDefName).To(Equal("override-net"))
			// Non-overridden fields remain from base
			Expect(result["request1"].Driver).To(Equal("vfio-pci"))
			Expect(result["request1"].IfName).To(Equal("eth0"))
		})
	})

	Context("Driver Filtering", func() {
		It("should skip configs for different drivers", func() {
			ourConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "our-net",
			}
			ourEncoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), ourConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: "other.driver.com",
							Parameters: runtime.RawExtension{
								Raw: []byte(`{"someField": "someValue"}`),
							},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: ourEncoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result["request1"].Driver).To(Equal("vfio-pci"))
		})

		It("should process only matching driver configs", func() {
			vfConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "test-net",
			}
			encoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vfConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: "gpu.driver.com",
							Parameters: runtime.RawExtension{
								Raw: []byte(`{"gpuConfig": "value"}`),
							},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request2"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: "disk.driver.com",
							Parameters: runtime.RawExtension{
								Raw: []byte(`{"diskConfig": "value"}`),
							},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request3"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded,
							},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result).To(HaveKey("request3"))
			Expect(result).NotTo(HaveKey("request1"))
			Expect(result).NotTo(HaveKey("request2"))
		})
	})

	Context("Error Cases", func() {
		It("should return error for invalid config source", func() {
			vfConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "test-net",
			}
			encoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vfConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   "InvalidSource",
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded,
							},
						},
					},
				},
			}

			_, err = getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid config source"))
		})

		It("should return error for nil Opaque configuration", func() {
			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: nil,
					},
				},
			}

			_, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("only opaque parameters are supported"))
		})

		It("should return error for invalid JSON in parameters", func() {
			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: []byte(`invalid json`),
							},
						},
					},
				},
			}

			_, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error decoding config parameters"))
		})

		It("should return error when no configs match driver", func() {
			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: "other.driver.com",
							Parameters: runtime.RawExtension{
								Raw: []byte(`{"field": "value"}`),
							},
						},
					},
				},
			}

			_, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("no configs constructed for driver"))
		})

		It("should return error for wrong config type", func() {
			// Use JSON for a type that isn't registered - will fail during decode
			wrongTypeJSON := []byte(`{
				"apiVersion": "v1",
				"kind": "Pod",
				"metadata": {
					"name": "test"
				}
			}`)

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"request1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: wrongTypeJSON,
							},
						},
					},
				},
			}

			_, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error decoding config parameters"))
		})
	})

	Context("Edge Cases", func() {
		It("should handle empty configs list", func() {
			configs := []resourceapi.DeviceAllocationConfiguration{}

			_, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("no configs constructed for driver"))
		})

		It("should handle config with empty requests list", func() {
			vfConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "test-net",
			}
			encoded, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), vfConfig)
			Expect(err).NotTo(HaveOccurred())

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{}, // Empty requests
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver: consts.DriverName,
							Parameters: runtime.RawExtension{
								Raw: encoded,
							},
						},
					},
				},
			}

			_, err = getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("no configs constructed for driver"))
		})

		It("should handle multiple class and claim configs with different requests", func() {
			classConfig1 := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				Driver:           "vfio-pci",
				NetAttachDefName: "class-net1",
			}
			classConfig2 := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				IfName: "eth0",
			}
			claimConfig := &configapi.VfConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "sriovnetwork.k8snetworkplumbingwg.io/v1alpha1",
					Kind:       "VfConfig",
				},
				NetAttachDefName: "claim-net",
			}

			class1Encoded, _ := runtime.Encode(configapi.Decoder.(runtime.Encoder), classConfig1)
			class2Encoded, _ := runtime.Encode(configapi.Decoder.(runtime.Encoder), classConfig2)
			claimEncoded, _ := runtime.Encode(configapi.Decoder.(runtime.Encoder), claimConfig)

			configs := []resourceapi.DeviceAllocationConfiguration{
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"req1", "req2"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver:     consts.DriverName,
							Parameters: runtime.RawExtension{Raw: class1Encoded},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClass,
					Requests: []string{"req2", "req3"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver:     consts.DriverName,
							Parameters: runtime.RawExtension{Raw: class2Encoded},
						},
					},
				},
				{
					Source:   resourceapi.AllocationConfigSourceClaim,
					Requests: []string{"req1"},
					DeviceConfiguration: resourceapi.DeviceConfiguration{
						Opaque: &resourceapi.OpaqueDeviceConfiguration{
							Driver:     consts.DriverName,
							Parameters: runtime.RawExtension{Raw: claimEncoded},
						},
					},
				},
			}

			result, err := getMapOfOpaqueDeviceConfigForDevice(decoder, configs)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(3))

			// req1: class config + claim override
			Expect(result["req1"].Driver).To(Equal("vfio-pci"))
			Expect(result["req1"].NetAttachDefName).To(Equal("claim-net"))

			// req2: both class configs applied
			Expect(result["req2"].Driver).To(Equal("vfio-pci"))
			Expect(result["req2"].NetAttachDefName).To(Equal("class-net1"))
			Expect(result["req2"].IfName).To(Equal("eth0"))

			// req3: only second class config
			Expect(result["req3"].IfName).To(Equal("eth0"))
		})
	})
})
