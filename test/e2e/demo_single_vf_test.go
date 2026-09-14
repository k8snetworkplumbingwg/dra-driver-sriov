//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/single-vf-claim", Label(framework.LabelStandalone), Serial, Ordered, func() {
	const (
		ns        = "vf-test1"
		podName   = "pod0"
		container = "ctr0"
		ifName    = "net1"
		podClaim  = "vf"
	)

	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			Namespaces: []string{ns},
			SriovResourcePolicies: []string{
				"all-devices",
			},
		})
	})

	It("allocates a single VF, brings up net1, and reclaims on delete", func() {
		clients.SkipUnlessStandalone(ctx)

		path, err := framework.DemoPath("single-vf-claim", "single-vf.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying fixture")
		_, err = clients.ApplyYAML(ctx, path)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pod Ready")
		pod := clients.WaitForPodReady(ctx, ns, podName)

		var claimName string
		for _, st := range pod.Status.ResourceClaimStatuses {
			if st.Name == podClaim && st.ResourceClaimName != nil {
				claimName = *st.ResourceClaimName
				break
			}
		}
		Expect(claimName).NotTo(BeEmpty(), "pod %s has no ResourceClaim name for %s", podName, podClaim)

		By("waiting for ResourceClaim allocation")
		clients.WaitForResourceClaimAllocated(ctx, ns, claimName)

		By("checking SR-IOV interface has an address")
		clients.ExpectInterfaceHasAddress(ctx, ns, podName, container, ifName)

		By("deleting pod to reclaim")
		err = clients.Clientset.CoreV1().Pods(ns).Delete(ctx, podName, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			_, err := clients.Clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "pod %s still exists: %v", podName, err)
		}).WithTimeout(framework.DefaultTimeout).WithPolling(framework.DefaultInterval).Should(Succeed())

		By("waiting for ResourceClaim to be reclaimed")
		Eventually(func(g Gomega) {
			_, err := clients.Dynamic.Resource(framework.ResourceClaimGVR).Namespace(ns).Get(ctx, claimName, metav1.GetOptions{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "ResourceClaim %s still exists: %v", claimName, err)
		}).WithTimeout(framework.DefaultTimeout).WithPolling(framework.DefaultInterval).Should(Succeed())
	})
})
