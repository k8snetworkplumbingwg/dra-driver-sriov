/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package types_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

var _ = Describe("IsPermanentStatusUpdateError", func() {
	gr := schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}

	It("treats responses that cannot succeed unchanged as permanent", func() {
		// Invalid is the duplicate-device-key case that used to retry to timeout.
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewInvalid(schema.GroupKind{}, "claim", nil))).To(BeTrue())
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewForbidden(gr, "claim", nil))).To(BeTrue())
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewNotFound(gr, "claim"))).To(BeTrue())
	})

	It("does not treat conflicts, transient or network errors as permanent", func() {
		// Conflict is handled by the caller (refetch and merge), the rest are worth
		// retrying, so none of these should stop the retry loop.
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewConflict(gr, "claim", nil))).To(BeFalse())
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewServerTimeout(gr, "update", 1))).To(BeFalse())
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewTooManyRequests("slow down", 1))).To(BeFalse())
		Expect(types.IsPermanentStatusUpdateError(apierrors.NewInternalError(errors.New("boom")))).To(BeFalse())
		Expect(types.IsPermanentStatusUpdateError(errors.New("dial tcp: connection refused"))).To(BeFalse())
	})
})

var _ = Describe("UpdateClaimStatusWithRetry", func() {
	const namespace = "default"
	gr := schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}
	// Short backoff so the retry-exhaustion case does not slow the suite down.
	fastBackoff := wait.Backoff{Steps: 4, Duration: time.Millisecond}

	newClaim := func(devices ...resourceapi.AllocatedDeviceStatus) *resourceapi.ResourceClaim {
		return &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "claim-a", Namespace: namespace, UID: k8stypes.UID("claim-a-uid")},
			Status:     resourceapi.ResourceClaimStatus{Devices: devices},
		}
	}
	ownDevice := func(data string) resourceapi.AllocatedDeviceStatus {
		return resourceapi.AllocatedDeviceStatus{
			Driver: consts.DriverName,
			Pool:   "pool-a",
			Device: "dev-a",
			Data:   &runtime.RawExtension{Raw: []byte(data)},
		}
	}
	foreignDevice := func(device string) resourceapi.AllocatedDeviceStatus {
		return resourceapi.AllocatedDeviceStatus{Driver: "other.example.com", Pool: "pool-x", Device: device}
	}
	deviceByDriver := func(devices []resourceapi.AllocatedDeviceStatus, driver string) *resourceapi.AllocatedDeviceStatus {
		for i := range devices {
			if devices[i].Driver == driver {
				return &devices[i]
			}
		}
		return nil
	}
	isStatusUpdate := func(action k8stesting.Action) bool {
		return action.GetVerb() == "update" && action.GetSubresource() == "status"
	}

	It("succeeds on the first attempt without a conflict", func() {
		claim := newClaim(ownDevice(`{"vf":1}`))
		fake := k8sfake.NewSimpleClientset(claim.DeepCopy())

		err := types.UpdateClaimStatusWithRetry(context.Background(), fake.ResourceV1().ResourceClaims(namespace), claim, consts.DriverName, fastBackoff)
		Expect(err).NotTo(HaveOccurred())
	})

	It("preserves another driver's entry written during the conflict window", func() {
		// The server already carries a foreign entry this driver never saw.
		server := newClaim(foreignDevice("dev-foreign"), ownDevice(`{"vf":1}`))
		fake := k8sfake.NewSimpleClientset(server.DeepCopy())

		// This driver's view has only its own device, updated to a new value.
		local := newClaim(ownDevice(`{"vf":2}`))

		firstUpdate := true
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			if firstUpdate {
				firstUpdate = false
				return true, nil, apierrors.NewConflict(gr, "claim-a", errors.New("conflict"))
			}
			return false, nil, nil
		})

		err := types.UpdateClaimStatusWithRetry(context.Background(), fake.ResourceV1().ResourceClaims(namespace), local, consts.DriverName, fastBackoff)
		Expect(err).NotTo(HaveOccurred())

		got, err := fake.ResourceV1().ResourceClaims(namespace).Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Status.Devices).To(HaveLen(2))

		foreign := deviceByDriver(got.Status.Devices, "other.example.com")
		Expect(foreign).NotTo(BeNil(), "foreign entry should survive the merge")
		Expect(foreign.Device).To(Equal("dev-foreign"))

		own := deviceByDriver(got.Status.Devices, consts.DriverName)
		Expect(own).NotTo(BeNil())
		Expect(own.Data).NotTo(BeNil())
		Expect(string(own.Data.Raw)).To(Equal(`{"vf":2}`), "own entry should hold this driver's desired value")
	})

	It("stops on a permanent refetch error instead of retrying to the timeout", func() {
		claim := newClaim(ownDevice(`{"vf":1}`))
		fake := k8sfake.NewSimpleClientset(claim.DeepCopy())

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			return true, nil, apierrors.NewConflict(gr, "claim-a", errors.New("conflict"))
		})
		getCalls := 0
		fake.PrependReactor("get", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			getCalls++
			return true, nil, apierrors.NewNotFound(gr, "claim-a")
		})

		err := types.UpdateClaimStatusWithRetry(context.Background(), fake.ResourceV1().ResourceClaims(namespace), claim, consts.DriverName, fastBackoff)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		// The early stop is what this test guards: one update, one refetch, no
		// looping until fastBackoff.Steps is exhausted.
		Expect(updateCalls).To(Equal(1), "the retry should stop after the first permanent refetch, not retry the update")
		Expect(getCalls).To(Equal(1), "the refetch should not be retried once it fails permanently")
	})

	It("returns the real error rather than the wait timeout when retries are exhausted", func() {
		claim := newClaim(ownDevice(`{"vf":1}`))
		fake := k8sfake.NewSimpleClientset(claim.DeepCopy())

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			return true, nil, apierrors.NewInternalError(errors.New("etcd unavailable"))
		})

		err := types.UpdateClaimStatusWithRetry(context.Background(), fake.ResourceV1().ResourceClaims(namespace), claim, consts.DriverName, fastBackoff)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInternalError(err)).To(BeTrue(), "the last real API error should be surfaced, not the generic wait timeout")
		// A transient error is retried to exhaustion, so every step is used.
		Expect(updateCalls).To(Equal(fastBackoff.Steps), "a retryable error should be retried until the backoff is exhausted")
	})

	It("recovers when a retryable update error clears on a later attempt", func() {
		claim := newClaim(ownDevice(`{"vf":1}`))
		fake := k8sfake.NewSimpleClientset(claim.DeepCopy())

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			if updateCalls == 1 {
				return true, nil, apierrors.NewServerTimeout(gr, "update", 1)
			}
			return false, nil, nil
		})

		err := types.UpdateClaimStatusWithRetry(context.Background(), fake.ResourceV1().ResourceClaims(namespace), claim, consts.DriverName, fastBackoff)
		Expect(err).NotTo(HaveOccurred(), "a transient error that clears should let the update succeed")
		Expect(updateCalls).To(Equal(2), "the update should be retried once and then succeed")
	})

	It("surfaces context cancellation rather than the last API error", func() {
		claim := newClaim(ownDevice(`{"vf":1}`))
		fake := k8sfake.NewSimpleClientset(claim.DeepCopy())
		ctx, cancel := context.WithCancel(context.Background())

		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			// Cancel while a real (retryable) API error is pending, so lastErr is set.
			cancel()
			return true, nil, apierrors.NewServerTimeout(gr, "update", 1)
		})

		err := types.UpdateClaimStatusWithRetry(ctx, fake.ResourceV1().ResourceClaims(namespace), claim, consts.DriverName, fastBackoff)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "a cancelled context is the reason the loop ended and should not be masked by lastErr")
	})
})
