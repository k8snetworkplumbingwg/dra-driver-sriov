//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

// smoke/advertise-devices is a non-demo check that the driver can publish VFs
// via a catch-all SriovResourcePolicy. Deploy no longer applies this policy.
var _ = Describe("smoke/advertise-devices", Serial, func() {
	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			SriovResourcePolicies: []string{"all-devices"},
		})
	})

	It("advertises VFs via all-devices SriovResourcePolicy", func() {
		By("applying catch-all all-devices SriovResourcePolicy")
		Expect(clients.ApplyAllDevicesPolicy(ctx)).To(Succeed())

		By("waiting for ResourceSlices to publish devices")
		clients.WaitForResourceSlicesWithDevices(ctx)
	})
})
