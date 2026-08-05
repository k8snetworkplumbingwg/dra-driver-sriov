//go:build e2e

package e2e_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/multiple-vf-claim", Serial, Ordered, func() {
	const (
		ns        = "vf-test3"
		podName   = "pod0"
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
		path, err := framework.DemoPath("multiple-vf-claim", "multiple-vf-one-claim.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pod Ready")
		clients.WaitForPodReady(ctx, ns, podName)

		By("checking SR-IOV interfaces exists")
		// multi-VF demo does not set ifName; driver auto-names the interfaces
		out, err := clients.ExecInPod(ctx, ns, podName, container, "ip", "-o", "link", "show")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).NotTo(BeEmpty())
		// claim requests 2 VFs: expect lo + eth0 + both workload interfaces
		linkLines := strings.Split(strings.TrimSpace(out), "\n")
		Expect(len(linkLines)).To(BeNumerically(">=", 4), "expected lo, eth0, and 2 VF interfaces:\n%s", out)
	})
})
