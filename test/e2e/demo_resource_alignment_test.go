//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/resource-alignment", Label(framework.LabelAlignment), Serial, Ordered, func() {
	const (
		ns      = "vf-test5"
		podName = "pod0"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"all-devices",
			},
		})
	})

	It("schedules a pod with PCIe root alignment constraints", func() {
		clients.SkipUnlessAlignment(ctx)

		path, err := framework.DemoPath("resource-alignment", "resource-alignment.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pod Ready")
		clients.WaitForPodReady(ctx, ns, podName)
	})
})
