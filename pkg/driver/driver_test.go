package driver

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
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
