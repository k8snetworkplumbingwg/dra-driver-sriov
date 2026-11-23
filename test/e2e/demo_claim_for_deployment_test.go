//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/claim-for-deployment", Label(framework.LabelStandalone), Serial, Ordered, func() {
	const (
		ns         = "vf-test6"
		deployName = "pod0"
		podClaim   = "vf"
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
		clients.SkipUnlessStandalone(ctx)

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

		By("waiting for the driver to record the network data on each replica's ResourceClaim")
		for _, name := range names {
			pod, err := clients.Clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			claimName := framework.ResourceClaimNameForPodClaim(pod, podClaim)
			Expect(claimName).NotTo(BeEmpty(), "pod %s has no ResourceClaim for %s", name, podClaim)
			clients.WaitForResourceClaimDeviceNetworkData(ctx, ns, claimName, ifName)
		}
	})
})
