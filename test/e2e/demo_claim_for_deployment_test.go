//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/claim-for-deployment", Serial, Ordered, func() {
	const (
		ns         = "vf-test6"
		deployName = "pod0"
		container  = "ctr0"
		ifName     = "net1"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"all-devices",
			},
		})
	})

	It("rolls out a Deployment with ResourceClaimTemplates", func() {
		path, err := framework.DemoPath("claim-for-deployment", "deployment-vf.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for Deployment ready")
		clients.WaitForDeploymentReady(ctx, ns, deployName)

		By("checking net1 on a pod replica")
		names, err := clients.PodNamesForDeployment(ctx, ns, deployName)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).NotTo(BeEmpty())
		clients.ExpectInterfaceHasAddress(ctx, ns, names[0], container, ifName)
	})
})
