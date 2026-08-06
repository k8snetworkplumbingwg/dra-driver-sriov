//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/resource-policies", Label(framework.LabelResourcePolicy, framework.LabelStandalone), Serial, Ordered, func() {
	const (
		ns        = "vf-test4"
		podName   = "pod0"
		container = "ctr0"
		ifName    = "net1"
		// Matches DeviceAttributes / CEL in demo/resource-policies/resource-policy.yaml
		resourceNameAttr = "sriovnetwork.k8snetworkplumbingwg.io/resourceName"
		eth1Resource     = "eth1_resource"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"example-resource-policy",
			},
			DeviceAttributes: []string{
				"eth0-attrs",
				"eth1-attrs",
			},
		})
	})

	It("applies resource policies and starts a filtered VF pod", func() {
		clients.SkipUnlessStandalone(ctx)
		clients.SkipIfNodeMissing(ctx, "dra-ctlplane-0.dra.lab")

		path, err := framework.DemoPath("resource-policies", "resource-policy.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for eth1_resource on ResourceSlice devices")
		clients.WaitForDeviceAttributeString(ctx, resourceNameAttr, eth1Resource)

		By("waiting for pod Ready")
		clients.WaitForPodReady(ctx, ns, podName)

		By("checking SR-IOV interface has an address")
		clients.ExpectInterfaceHasAddress(ctx, ns, podName, container, ifName)
	})
})
