package driver

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
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
			flags := &types.Flags{KubeletPluginsDirectoryPath: "/tmp"}
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
})
