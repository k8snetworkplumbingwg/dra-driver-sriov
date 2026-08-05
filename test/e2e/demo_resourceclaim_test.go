//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/resourceclaim", Serial, Ordered, func() {
	const (
		ns        = "vf-test7"
		podName   = "pod0"
		container = "ctr0"
		ifName    = "net1"
		claimName = "static-vf-claim"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"all-devices",
			},
		})
	})

	It("binds a static ResourceClaim and brings up net1", func() {
		path, err := framework.DemoPath("resourceclaim", "resourceclaim-vf.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for claim allocation")
		clients.WaitForResourceClaimAllocated(ctx, ns, claimName)

		By("waiting for pod Ready")
		clients.WaitForPodReady(ctx, ns, podName)

		By("checking SR-IOV interface has an address")
		clients.ExpectInterfaceHasAddress(ctx, ns, podName, container, ifName)
	})
})
