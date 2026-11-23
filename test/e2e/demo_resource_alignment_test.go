//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/resource-alignment", Label(framework.LabelStandalone, framework.LabelAlignment), Serial, Ordered, func() {
	const (
		ns               = "vf-test5"
		podName          = "pod0"
		podClaim         = "vf"
		ifName           = "net1"
		vfDeviceRequest  = "vf"
		gpuDeviceRequest = "gpu"
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
		clients.SkipUnlessStandalone(ctx)
		clients.SkipUnlessAlignment(ctx)

		path, err := framework.DemoPath("resource-alignment", "resource-alignment.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pod Ready")
		pod := clients.WaitForPodReady(ctx, ns, podName)

		claimName := framework.ResourceClaimNameForPodClaim(pod, podClaim)
		Expect(claimName).NotTo(BeEmpty(), "pod %s has no ResourceClaim for %s", podName, podClaim)

		By("checking VF and GPU share pcieRoot via ResourceSlice attributes")
		clients.ExpectResourceClaimRequestsSharePCIeRoot(ctx, ns, claimName, vfDeviceRequest, gpuDeviceRequest)

		By("waiting for the driver to record the VF's network data on the ResourceClaim")
		clients.WaitForResourceClaimDeviceNetworkData(ctx, ns, claimName, ifName)
	})
})
