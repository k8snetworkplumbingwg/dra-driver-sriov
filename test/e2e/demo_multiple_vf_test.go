//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/multiple-vf-claim", Label(framework.LabelStandalone), Serial, Ordered, func() {
	const (
		ns        = "vf-test3"
		podName   = "pod0"
		podClaim  = "vf"
		container = "ctr0"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"all-devices",
			},
		})
	})

	It("allocates multiple VFs in one claim and starts the pod", func() {
		clients.SkipUnlessStandalone(ctx)

		path, err := framework.DemoPath("multiple-vf-claim", "multiple-vf-one-claim.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pod Ready")
		pod := clients.WaitForPodReady(ctx, ns, podName)

		By("checking SR-IOV interfaces exists")
		// multi-VF demo omits ifName; driver auto-names interfaces vfnet0, vfnet1
		clients.ExpectPodLinkInterfacesExact(ctx, ns, podName, container, "lo", "eth0", "vfnet0", "vfnet1")

		claimName := framework.ResourceClaimNameForPodClaim(pod, podClaim)
		Expect(claimName).NotTo(BeEmpty(), "pod %s has no ResourceClaim for %s", podName, podClaim)

		By("waiting for the driver to record the network data of both VFs on the ResourceClaim")
		clients.WaitForResourceClaimDeviceNetworkData(ctx, ns, claimName, "vfnet0")
		clients.WaitForResourceClaimDeviceNetworkData(ctx, ns, claimName, "vfnet1")
	})
})
