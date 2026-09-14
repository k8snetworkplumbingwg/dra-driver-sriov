//go:build e2e

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/test/e2e/framework"
)

var _ = Describe("demo/extended-resource", Label(framework.LabelExtendedResource), Serial, Ordered, func() {
	AfterEach(func() {
		clients.Cleanup(ctx, framework.CleanupSpec{
			DeviceClasses: []string{
				"sriov-port1",
				"sriov-port2",
			},
			DeviceAttributes: []string{
				"port1-attrs",
				"port2-attrs",
			},
			SriovResourcePolicies: []string{
				// Fixture replaces advertising with dual-port-vfs; wipe both so nothing leaks.
				"all-devices",
				"dual-port-vfs",
			},
			DefaultPods: []string{
				"ext-resource-dual-port",
				"ext-resource-dual-port-2x2",
			},
			DefaultNADs: []string{
				"sriov-port1-net",
				"sriov-port2-net",
			},
		})
	})

	It("schedules pods via DRA extended resources", func() {
		deviceClassPath, err := framework.DemoPath("extended-resource", "deviceclass.yaml")
		Expect(err).NotTo(HaveOccurred())
		podPath, err := framework.DemoPath("extended-resource", "pod-extended-resource.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("applying DeviceClasses and NADs")
		_, err = clients.ApplyYAML(ctx, deviceClassPath)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for port1-vfs and port2-vfs on ResourceSlice devices")
		const resourceNameAttr = "sriovnetwork.k8snetworkplumbingwg.io/resourceName"
		clients.WaitForDeviceAttributeString(ctx, resourceNameAttr, "port1-vfs")
		clients.WaitForDeviceAttributeString(ctx, resourceNameAttr, "port2-vfs")

		By("applying extended-resource pods")
		_, err = clients.ApplyYAML(ctx, podPath)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for pods Ready")
		clients.WaitForPodReady(ctx, "default", "ext-resource-dual-port")
		clients.WaitForPodReady(ctx, "default", "ext-resource-dual-port-2x2")
	})
})
