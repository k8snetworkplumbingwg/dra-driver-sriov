package driver

import (
	"context"
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	metadatav1alpha1 "k8s.io/dynamic-resource-allocation/api/metadata/v1alpha1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

var _ = Describe("Driver", func() {
	Context("PrepareResourceClaims orchestrator", func() {
		It("returns immediately with empty input", func() {
			d := &Driver{}
			result, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeEmpty())
		})

		It("errors when no prepared devices exist for the pod after processing", func() {
			flags := &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}
			cfg := &types.Config{Flags: flags}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).ToNot(HaveOccurred())

			d := &Driver{podManager: pm}

			// Claim with ReservedFor but no Allocation -> inner prepare will error, then final GetDevicesByPodUID fails
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc1", UID: k8stypes.UID("rc-uid")}}
			claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("pod-uid")}}

			_, err = d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no prepared devices found for pod"))
		})

		It("returns error instead of panicking when no claim contains pod info", func() {
			flags := &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}
			cfg := &types.Config{Flags: flags}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).ToNot(HaveOccurred())

			d := &Driver{podManager: pm}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "rc1",
					UID:       k8stypes.UID("rc-uid"),
				},
			}

			_, err = d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no pod info found for prepared claims"))
		})
	})

	Context("prepareResourceClaim guards", func() {
		It("errors when ReservedFor is empty", func() {
			d := &Driver{}
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")}}
			res := d.prepareResourceClaim(context.Background(), new(int), claim)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("no pod info found"))
		})

		It("errors when multiple pods in ReservedFor", func() {
			d := &Driver{}
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")}}
			claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: "a"}, {UID: "b"}}
			res := d.prepareResourceClaim(context.Background(), new(int), claim)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("multiple pods"))
		})

		It("errors when Allocation is nil", func() {
			d := &Driver{}
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")}}
			claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("pod-uid")}}
			res := d.prepareResourceClaim(context.Background(), new(int), claim)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("claim not yet allocated"))
		})

		It("returns cached prepared devices before MAC/pod lookup", func() {
			tempDir, err := os.MkdirTemp("", "driver-podmanager-*")
			Expect(err).ToNot(HaveOccurred())
			defer func() {
				Expect(os.RemoveAll(tempDir)).To(Succeed())
			}()

			flags := &types.Flags{KubeletPluginsDirectoryPath: tempDir}
			cfg := &types.Config{Flags: flags}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).ToNot(HaveOccurred())

			podUID := k8stypes.UID("pod-uid")
			claimUID := k8stypes.UID("claim-uid")
			err = pm.Set(podUID, claimUID, types.PreparedDevices{
				&types.PreparedDevice{},
			})
			Expect(err).ToNot(HaveOccurred())

			d := &Driver{
				podManager: pm,
				// Intentionally no pod object in fake client; this would fail if pod lookup runs.
				client: k8sfake.NewSimpleClientset(),
			}

			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: claimUID},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Name: "missing-pod", UID: podUID}},
					Allocation:  &resourceapi.AllocationResult{},
				},
			}

			res := d.prepareResourceClaim(context.Background(), new(int), claim)
			Expect(res.Err).NotTo(HaveOccurred())
			Expect(res.Devices).To(HaveLen(1))
		})
	})

	Context("HandleError", func() {
		It("calls cancelCtx on fatal errors", func() {
			called := false
			d := &Driver{cancelCtx: func(err error) { called = true }}
			d.HandleError(context.Background(), fmt.Errorf("fatal"), "oops")
			Expect(called).To(BeTrue())
		})
		It("does not cancel on recoverable errors", func() {
			called := false
			d := &Driver{cancelCtx: func(err error) { called = true }}
			d.HandleError(context.Background(), kubeletplugin.ErrRecoverable, "oops")
			Expect(called).To(BeFalse())
		})
	})

	Context("resolvePodClaimNameForResourceClaim", func() {
		It("resolves claim name from pod.spec.resourceClaims for direct claims", func() {
			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:              "claim-ref",
							ResourceClaimName: ptrTo("real-claim-name"),
						},
					},
				},
			}

			Expect(resolvePodClaimNameForResourceClaim(pod, "real-claim-name")).To(Equal("claim-ref"))
		})

		It("resolves claim name from pod.status.resourceClaimStatuses for template-backed claims", func() {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{
					ResourceClaimStatuses: []corev1.PodResourceClaimStatus{
						{
							Name:              "claim-ref",
							ResourceClaimName: ptrTo("generated-claim-name"),
						},
					},
				},
			}

			Expect(resolvePodClaimNameForResourceClaim(pod, "generated-claim-name")).To(Equal("claim-ref"))
		})

		It("returns empty string when claim cannot be resolved", func() {
			pod := &corev1.Pod{}
			Expect(resolvePodClaimNameForResourceClaim(pod, "missing-claim")).To(BeEmpty())
		})
	})

	Context("getMACAddressesForClaim", func() {
		It("returns only MAC addresses that belong to the claim", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "launcher-pod",
					Namespace: "default",
					Annotations: map[string]string{
						types.DRANetworkMACsAnnotation: `{"claim-ref/vf":"02:00:00:aa:bb:cc","other-claim/vf":"02:00:00:dd:ee:ff"}`,
					},
				},
				Spec: corev1.PodSpec{
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:              "claim-ref",
							ResourceClaimName: ptrTo("real-claim-name"),
						},
					},
				},
			}
			d := &Driver{
				client: k8sfake.NewSimpleClientset(pod),
			}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "real-claim-name",
					Namespace: "default",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Name: "launcher-pod"}},
				},
			}

			macs, err := d.getMACAddressesForClaim(context.Background(), claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(macs).To(HaveKeyWithValue("vf", "02:00:00:aa:bb:cc"))
			Expect(macs).NotTo(HaveKey("claim-ref/vf"))
			Expect(macs).NotTo(HaveKey("other-claim/vf"))
			Expect(claim.Annotations).To(BeNil())
		})

		It("fails when MAC annotation exists but claim alias cannot be resolved", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "launcher-pod",
					Namespace: "default",
					Annotations: map[string]string{
						types.DRANetworkMACsAnnotation: `{"claim-ref/vf":"02:00:00:aa:bb:cc"}`,
					},
				},
			}
			d := &Driver{
				client: k8sfake.NewSimpleClientset(pod),
			}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "real-claim-name",
					Namespace: "default",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Name: "launcher-pod"}},
				},
			}

			_, err := d.getMACAddressesForClaim(context.Background(), claim)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to resolve pod claim name"))
		})
	})

	Context("buildPluginOptions", func() {
		var (
			oldEnableDeviceMetadataOption func(bool) kubeletplugin.Option
			oldCDIDirectoryOption         func(string) kubeletplugin.Option
			oldMetadataVersionsOption     func(...schema.GroupVersion) kubeletplugin.Option
		)

		BeforeEach(func() {
			oldEnableDeviceMetadataOption = enableDeviceMetadataOption
			oldCDIDirectoryOption = cdiDirectoryOption
			oldMetadataVersionsOption = metadataVersionsOption
		})

		AfterEach(func() {
			enableDeviceMetadataOption = oldEnableDeviceMetadataOption
			cdiDirectoryOption = oldCDIDirectoryOption
			metadataVersionsOption = oldMetadataVersionsOption
		})

		It("uses configured CDI root when metadata is enabled", func() {
			flags := &types.Flags{
				NodeName:                      "node-a",
				CdiRoot:                       "/tmp/custom-cdi",
				KubeletRegistrarDirectoryPath: "/tmp/registry",
				KubeletPluginsDirectoryPath:   "/tmp/plugins",
				EnableDeviceMetadata:          true,
			}
			cfg := &types.Config{Flags: flags}

			metadataEnabledCalled := false
			cdiDir := ""
			metadataVersionCalled := false

			enableDeviceMetadataOption = func(enabled bool) kubeletplugin.Option {
				metadataEnabledCalled = enabled
				return oldEnableDeviceMetadataOption(enabled)
			}
			cdiDirectoryOption = func(path string) kubeletplugin.Option {
				cdiDir = path
				return oldCDIDirectoryOption(path)
			}
			metadataVersionsOption = func(versions ...schema.GroupVersion) kubeletplugin.Option {
				if len(versions) == 1 && versions[0] == metadatav1alpha1.SchemeGroupVersion {
					metadataVersionCalled = true
				}
				return oldMetadataVersionsOption(versions...)
			}

			opts := buildPluginOptions(cfg)
			Expect(opts).To(HaveLen(8))
			Expect(metadataEnabledCalled).To(BeTrue())
			Expect(cdiDir).To(Equal("/tmp/custom-cdi"))
			Expect(metadataVersionCalled).To(BeTrue())
		})

		It("does not append metadata options when metadata is disabled", func() {
			flags := &types.Flags{
				NodeName:                      "node-a",
				CdiRoot:                       "/tmp/custom-cdi",
				KubeletRegistrarDirectoryPath: "/tmp/registry",
				KubeletPluginsDirectoryPath:   "/tmp/plugins",
				EnableDeviceMetadata:          false,
			}
			cfg := &types.Config{Flags: flags}

			metadataEnabledCalled := false
			cdiCalled := false
			metadataVersionCalled := false

			enableDeviceMetadataOption = func(enabled bool) kubeletplugin.Option {
				metadataEnabledCalled = true
				return oldEnableDeviceMetadataOption(enabled)
			}
			cdiDirectoryOption = func(path string) kubeletplugin.Option {
				cdiCalled = true
				return oldCDIDirectoryOption(path)
			}
			metadataVersionsOption = func(versions ...schema.GroupVersion) kubeletplugin.Option {
				metadataVersionCalled = true
				return oldMetadataVersionsOption(versions...)
			}

			opts := buildPluginOptions(cfg)
			Expect(opts).To(HaveLen(5))
			Expect(metadataEnabledCalled).To(BeFalse())
			Expect(cdiCalled).To(BeFalse())
			Expect(metadataVersionCalled).To(BeFalse())
		})
	})
})

func ptrTo(s string) *string {
	return &s
}
