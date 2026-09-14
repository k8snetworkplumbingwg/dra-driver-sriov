//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/vfio-driver", Label(framework.LabelVfio), Serial, Ordered, func() {
	const (
		ns        = "vf-test2"
		podName   = "pod0"
		container = "ctr0"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"all-devices",
			},
			DeviceClasses: []string{"vf-test2"},
		})
	})

	It("binds a VF to vfio-pci and starts the pod", func() {
		path, err := framework.DemoPath("vfio-driver", "vfio-driver-config.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pod Ready")
		clients.WaitForPodReady(ctx, ns, podName)

		By("checking VFIO device nodes are present")
		clients.ExpectPathExists(ctx, ns, podName, container, "/dev/vfio")
	})
})
