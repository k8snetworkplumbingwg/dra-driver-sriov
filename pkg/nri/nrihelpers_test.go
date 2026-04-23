package nri

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/containerd/nri/pkg/api"
)

var _ = Describe("NRI Helpers", func() {
	Context("getNetworkNamespace", func() {
		It("extracts network namespace path from pod sandbox", func() {
			pod := &api.PodSandbox{
				Linux: &api.LinuxPodSandbox{Namespaces: []*api.LinuxNamespace{{Type: "network", Path: "/proc/1/ns/net"}}},
			}
			Expect(getNetworkNamespace(pod)).To(Equal("/proc/1/ns/net"))
		})

		It("returns empty string when network namespace is missing", func() {
			pod := &api.PodSandbox{Linux: &api.LinuxPodSandbox{Namespaces: []*api.LinuxNamespace{{Type: "uts", Path: "/proc/1/ns/uts"}}}}
			Expect(getNetworkNamespace(pod)).To(Equal(""))
		})
	})

	Context("injectDeviceIDRuntimeConfig", func() {
		It("adds runtimeConfig.deviceID to netconf", func() {
			original := `{"cniVersion":"1.0.0","type":"host-device","name":"net1"}`

			updated, err := injectDeviceIDRuntimeConfig(original, "0000:29:00.0")
			Expect(err).NotTo(HaveOccurred())

			configMap := map[string]interface{}{}
			Expect(json.Unmarshal([]byte(updated), &configMap)).To(Succeed())

			runtimeConfig, exists := configMap["runtimeConfig"]
			Expect(exists).To(BeTrue())
			runtimeConfigMap, ok := runtimeConfig.(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(runtimeConfigMap["deviceID"]).To(Equal("0000:29:00.0"))
		})

		It("updates an existing runtimeConfig.deviceID", func() {
			original := `{"type":"host-device","runtimeConfig":{"deviceID":"old","foo":"bar"}}`

			updated, err := injectDeviceIDRuntimeConfig(original, "0000:af:00.5")
			Expect(err).NotTo(HaveOccurred())

			configMap := map[string]interface{}{}
			Expect(json.Unmarshal([]byte(updated), &configMap)).To(Succeed())
			runtimeConfigMap := configMap["runtimeConfig"].(map[string]interface{})
			Expect(runtimeConfigMap["deviceID"]).To(Equal("0000:af:00.5"))
			Expect(runtimeConfigMap["foo"]).To(Equal("bar"))
		})

		It("returns an error when runtimeConfig is not an object", func() {
			invalid := `{"type":"host-device","runtimeConfig":"oops"}`

			_, err := injectDeviceIDRuntimeConfig(invalid, "0000:af:00.6")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("runtimeConfig must be a JSON object"))
		})
	})
})
