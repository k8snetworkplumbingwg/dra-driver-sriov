//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/multus-integration-multiple-resourceclaim", Label(framework.LabelMultus), Serial, Ordered, func() {
	const (
		ns         = "vf-test9"
		deployName = "pod0"
		container  = "ctr0"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			DeviceAttributes: []string{
				"sriov1-attrs",
				"sriov2-attrs",
			},
			SriovResourcePolicies: []string{
				"all-devices",
				"multus-two-pools",
			},
		})
	})

	It("deploys a Multus workload with multiple ResourceClaims", func() {
		clients.SkipUnlessMultus(ctx)

		path, err := framework.DemoPath("multus-integration-multiple-resourceclaim", "multus-integration-multiple-resourceclaim.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for Deployment ready")
		clients.WaitForDeploymentReady(ctx, ns, deployName)

		names, err := clients.PodNamesForDeployment(ctx, ns, deployName)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).NotTo(BeEmpty())

		By("checking secondary network interfaces exist")
		out, err := clients.ExecInPod(ctx, ns, names[0], container, "ip", "-o", "link", "show")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).NotTo(BeEmpty())
		// Multus annotation vf-test1,vf-test2 attaches as net1 and net2
		Expect(out).To(ContainSubstring("net1"), "expected Multus interface net1:\n%s", out)
		Expect(out).To(ContainSubstring("net2"), "expected Multus interface net2:\n%s", out)
	})
})
