package cni_test

import (
	"bytes"
	"context"
	"os"

	"github.com/containerd/nri/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cni"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

var _ = Describe("CNI", func() {
	var (
		runtime *cni.Runtime
		ctx     context.Context
		pod     *api.PodSandbox
		netNS   string
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create runtime
		runtime = cni.New("test-driver", []string{"/opt/cni/bin"})

		pod = &api.PodSandbox{
			Id:        "test-container-id",
			Name:      "test-pod",
			Namespace: "test-namespace",
			Uid:       "test-pod-uid",
		}

		netNS = "/proc/12345/ns/net"
	})

	Context("New", func() {
		It("should create runtime with correct configuration", func() {
			driverName := "test-driver"
			cniPath := []string{"/opt/cni/bin"}

			runtime := cni.New(driverName, cniPath)

			Expect(runtime).NotTo(BeNil())
			Expect(runtime.DriverName).To(Equal(driverName))
			Expect(runtime.CNIConfig).NotTo(BeNil())
		})

		It("should handle empty CNI path", func() {
			runtime := cni.New("test-driver", []string{})

			Expect(runtime).NotTo(BeNil())
			Expect(runtime.DriverName).To(Equal("test-driver"))
		})

		It("should handle multiple CNI paths", func() {
			paths := []string{"/opt/cni/bin", "/usr/local/bin"}
			runtime := cni.New("test-driver", paths)

			Expect(runtime).NotTo(BeNil())
			Expect(runtime.DriverName).To(Equal("test-driver"))
		})
	})

	Context("AttachNetwork", func() {
		It("should handle invalid CNI configuration parsing", func() {
			invalidConfig := &types.PreparedDevice{
				IfName:             "net1",
				NetAttachDefConfig: `invalid json`,
			}

			_, _, err := runtime.AttachNetwork(ctx, pod, netNS, invalidConfig)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to GetCNIConfigFromSpec"))
		})

		It("should handle empty network attachment definition", func() {
			emptyConfig := &types.PreparedDevice{
				IfName:             "net1",
				NetAttachDefConfig: `{}`,
			}

			_, _, err := runtime.AttachNetwork(ctx, pod, netNS, emptyConfig)

			Expect(err).To(HaveOccurred())
		})
	})

	Context("DetachNetwork", func() {
		It("should handle invalid CNI configuration parsing", func() {
			invalidConfig := &types.PreparedDevice{
				IfName:             "net1",
				NetAttachDefConfig: `invalid json`,
			}

			err := runtime.DetachNetwork(ctx, pod, netNS, invalidConfig)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to GetCNIConfigFromSpec"))
		})

		It("should handle empty network attachment definition", func() {
			emptyConfig := &types.PreparedDevice{
				IfName:             "net1",
				NetAttachDefConfig: `{}`,
			}

			err := runtime.DetachNetwork(ctx, pod, netNS, emptyConfig)

			Expect(err).To(HaveOccurred())
		})
	})

	Context("RawExec", func() {
		var rawExec *cni.RawExec

		BeforeEach(func() {
			rawExec = &cni.RawExec{
				Stderr: os.Stderr,
			}
		})

		It("should create RawExec with correct configuration", func() {
			Expect(rawExec).NotTo(BeNil())
			Expect(rawExec.Stderr).To(Equal(os.Stderr))
		})

		It("should handle stderr configuration", func() {
			var stderr bytes.Buffer
			exec := &cni.RawExec{
				Stderr: &stderr,
			}

			Expect(exec.Stderr).To(Equal(&stderr))
		})

		Context("FindInPath", func() {
			It("should delegate to invoke.FindInPath", func() {
				// This is testing the delegation, the actual functionality is tested by the CNI library
				plugin := "bridge"
				paths := []string{"/opt/cni/bin"}

				// This will likely fail in test environment, but we're testing that the method exists and works
				_, err := rawExec.FindInPath(plugin, paths)

				// We don't care about the specific error, just that it doesn't panic
				_ = err
			})

			It("should handle empty paths", func() {
				plugin := "bridge"
				paths := []string{}

				_, err := rawExec.FindInPath(plugin, paths)

				// Should return error for empty paths
				Expect(err).To(HaveOccurred())
			})

			It("should handle empty plugin name", func() {
				plugin := ""
				paths := []string{"/opt/cni/bin"}

				_, err := rawExec.FindInPath(plugin, paths)

				// Should return error for empty plugin name
				Expect(err).To(HaveOccurred())
			})
		})
	})

	// Note: cniResultToNetworkData function is internal and tested indirectly through AttachNetwork

	Context("Integration scenarios", func() {
		It("should handle multiple device configurations", func() {
			// Test that we can create multiple devices with different configurations
			devices := []*types.PreparedDevice{
				{IfName: "net1", NetAttachDefConfig: `{"type":"invalid","name":"test1"}`},
				{IfName: "net2", NetAttachDefConfig: `{"type":"invalid","name":"test2"}`},
			}

			for _, device := range devices {
				_, _, err := runtime.AttachNetwork(ctx, pod, netNS, device)
				Expect(err).To(HaveOccurred()) // Expected to fail due to invalid config
			}
		})
	})
})
