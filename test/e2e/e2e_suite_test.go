//go:build e2e

package e2e_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	framework.SetSkipFunc(func(message string) { Skip(message) })
	RunSpecs(t, "DRA driver SR-IOV workload e2e suite")
}

var (
	clients *framework.Clients
	ctx     context.Context
)

// SynchronizedBeforeSuite runs once before any specs.
var _ = SynchronizedBeforeSuite(func() []byte {
	// This first func runs on process 1 and verifies the cluster is ready for
	// workload tests (driver pods present).
	c, err := framework.NewClients()
	Expect(err).NotTo(HaveOccurred())
	clients = c
	ctx = context.Background()

	By("checking driver pods exist")
	pods, err := clients.Clientset.CoreV1().Pods(framework.DriverNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: framework.DriverLabel,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).NotTo(BeEmpty(), "no DRA driver pods in %s", framework.DriverNamespace)

	return nil
}, func(_ []byte) {
	// This second func runs on every process (including process 1) to ensure each has a
	// clients/ctx handle; with serial single-process runs the first func already set them,
	// so this is a no-op unless clients is still nil (e.g. parallel workers).
	if clients == nil {
		c, err := framework.NewClients()
		Expect(err).NotTo(HaveOccurred())
		clients = c
		ctx = context.Background()
	}
})

var _ = ReportAfterEach(func(specReport SpecReport) {
	if specReport.Failed() && clients != nil {
		clients.DebugDump(ctx)
	}
})
