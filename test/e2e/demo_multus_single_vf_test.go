//go:build e2e

package e2e_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/multus-integration-single-vf", Label(framework.LabelMultus), Serial, Ordered, func() {
	const (
		ns         = "vf-test7"
		deployName = "pod0"
		container  = "ctr0"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			DeviceAttributes: []string{
				"sriov-nic-1-attrs",
			},
			SriovResourcePolicies: []string{
				// Multus demo replaces catch-all advertising; wipe both.
				"all-devices",
				"sriov-nic-1-policy",
			},
		})
	})

	It("deploys a Multus-annotated workload with a single VF claim", func() {
		clients.SkipUnlessMultus(ctx)

		path, err := framework.DemoPath("multus-integration-single-vf", "multus-integration-single-vf.yaml")
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
		// Multus NAD attaches one secondary interface beyond lo + eth0
		linkLines := strings.Split(strings.TrimSpace(out), "\n")
		Expect(len(linkLines)).To(BeNumerically(">=", 3), "expected lo, eth0, and 1 VF interface:\n%s", out)
	})
})
