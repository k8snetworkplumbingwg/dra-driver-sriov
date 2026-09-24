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
	const (
		namespace = "default"
		claimName = "claim-a"
		claimUID  = k8stypes.UID("claim-a-uid")
	)
	gr := schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}
	// Short backoff so the retry-exhaustion case does not slow the suite down.
	fastBackoff := wait.Backoff{Steps: 4, Duration: time.Millisecond}

	newClaim := func(devices ...resourceapi.AllocatedDeviceStatus) *resourceapi.ResourceClaim {
		return &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: namespace, UID: claimUID},
			Status:     resourceapi.ResourceClaimStatus{Devices: devices},
		}
	}
	ownDevice := func(device, data string) resourceapi.AllocatedDeviceStatus {
		return resourceapi.AllocatedDeviceStatus{
			Driver: consts.DriverName,
			Pool:   "pool-a",
			Device: device,
			Data:   &runtime.RawExtension{Raw: []byte(data)},
		}
	}
	foreignDevice := func(device string) resourceapi.AllocatedDeviceStatus {
		return resourceapi.AllocatedDeviceStatus{Driver: "other.example.com", Pool: "pool-x", Device: device}
	}
	deviceNamed := func(devices []resourceapi.AllocatedDeviceStatus, driver, device string) *resourceapi.AllocatedDeviceStatus {
		for i := range devices {
			if devices[i].Driver == driver && devices[i].Device == device {
				return &devices[i]
			}
		}
		return nil
	}
	isStatusUpdate := func(action k8stesting.Action) bool {
		return action.GetVerb() == "update" && action.GetSubresource() == "status"
	}
	// upsert is the prepare path's mutation for one device.
	upsert := func(device resourceapi.AllocatedDeviceStatus) types.ClaimStatusMutation {
		return types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{device})
	}
	update := func(ctx context.Context, fake *k8sfake.Clientset, mutate types.ClaimStatusMutation) error {
		return types.UpdateClaimStatusWithRetry(ctx, fake.ResourceV1().ResourceClaims(namespace), claimName, claimUID, fastBackoff, mutate)
	}
	get := func(fake *k8sfake.Clientset) *resourceapi.ResourceClaim {
		got, err := fake.ResourceV1().ResourceClaims(namespace).Get(context.Background(), claimName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		return got
	}
	// conflictOnce makes the first status update fail with a conflict, as a
	// concurrent writer would cause, and counts the update attempts.
	conflictOnce := func(fake *k8sfake.Clientset, updateCalls *int) {
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			*updateCalls++
			if *updateCalls == 1 {
				return true, nil, apierrors.NewConflict(gr, claimName, errors.New("conflict"))
			}
			return false, nil, nil
		})
	}

	It("applies the mutation to the fetched claim and writes it once", func() {
		fake := k8sfake.NewSimpleClientset(newClaim(foreignDevice("dev-foreign")))

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if isStatusUpdate(action) {
				updateCalls++
			}
			return false, nil, nil
		})

		Expect(update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))).To(Succeed())
		Expect(updateCalls).To(Equal(1))

		got := get(fake)
		Expect(deviceNamed(got.Status.Devices, "other.example.com", "dev-foreign")).NotTo(BeNil())
		Expect(deviceNamed(got.Status.Devices, consts.DriverName, "dev-a")).NotTo(BeNil())
	})

	It("does not write when the mutation changes nothing", func() {
		fake := k8sfake.NewSimpleClientset(newClaim(ownDevice("dev-a", `{"vf":1}`)))

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if isStatusUpdate(action) {
				updateCalls++
			}
			return false, nil, nil
		})

		Expect(update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))).To(Succeed())
		Expect(updateCalls).To(BeZero(), "an update that is already applied should not be written again")
	})

	It("preserves another driver's entry written during the conflict window", func() {
		// The server carries a foreign entry; the conflict stands in for it
		// having been written after this driver's first read.
		fake := k8sfake.NewSimpleClientset(newClaim(foreignDevice("dev-foreign"), ownDevice("dev-a", `{"vf":1}`)))
		updateCalls := 0
		conflictOnce(fake, &updateCalls)

		Expect(update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":2}`)))).To(Succeed())
		Expect(updateCalls).To(Equal(2))

		got := get(fake)
		Expect(got.Status.Devices).To(HaveLen(2))
		Expect(deviceNamed(got.Status.Devices, "other.example.com", "dev-foreign")).NotTo(BeNil(), "foreign entry should survive the retry")
		own := deviceNamed(got.Status.Devices, consts.DriverName, "dev-a")
		Expect(own).NotTo(BeNil())
		Expect(string(own.Data.Raw)).To(Equal(`{"vf":2}`), "own entry should hold this driver's desired value")
	})

	It("preserves this driver's own entries written during the conflict window", func() {
		// Between this driver's first read and its retry, its other status
		// writer updated dev-b and added dev-c. A retry that restored the
		// pre-conflict snapshot reverted dev-b and dropped dev-c.
		fake := k8sfake.NewSimpleClientset(newClaim(ownDevice("dev-a", `{"vf":1}`), ownDevice("dev-b", `{"vf":1}`)))

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			if updateCalls > 1 {
				return false, nil, nil
			}
			// The concurrent writer lands first; the attempt under test conflicts.
			concurrent := newClaim(ownDevice("dev-a", `{"vf":1}`), ownDevice("dev-b", `{"vf":99}`), ownDevice("dev-c", `{"vf":3}`))
			Expect(fake.Tracker().Update(resourceapi.SchemeGroupVersion.WithResource("resourceclaims"), concurrent, namespace)).To(Succeed())
			return true, nil, apierrors.NewConflict(gr, claimName, errors.New("conflict"))
		})

		Expect(update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":2}`)))).To(Succeed())
		Expect(updateCalls).To(Equal(2))

		got := get(fake)
		Expect(got.Status.Devices).To(HaveLen(3))
		Expect(string(deviceNamed(got.Status.Devices, consts.DriverName, "dev-a").Data.Raw)).To(Equal(`{"vf":2}`))
		Expect(string(deviceNamed(got.Status.Devices, consts.DriverName, "dev-b").Data.Raw)).To(Equal(`{"vf":99}`), "an untouched own entry must not be reverted")
		Expect(deviceNamed(got.Status.Devices, consts.DriverName, "dev-c")).NotTo(BeNil(), "a same-driver entry added concurrently must survive")
	})

	It("retries a burst of conflicts without burning the backoff", func() {
		// Six conflicts in a row exceed fastBackoff.Steps; a conflict is retried
		// right away within the step rather than counted as one.
		fake := k8sfake.NewSimpleClientset(newClaim())

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			if updateCalls <= 6 {
				return true, nil, apierrors.NewConflict(gr, claimName, errors.New("conflict"))
			}
			return false, nil, nil
		})

		Expect(update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))).To(Succeed())
		Expect(updateCalls).To(Equal(7))
		Expect(deviceNamed(get(fake).Status.Devices, consts.DriverName, "dev-a")).NotTo(BeNil())
	})

	It("reapplies the mutation to the refetched claim on every attempt", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())
		updateCalls := 0
		conflictOnce(fake, &updateCalls)

		var seen []string
		mutate := func(claim *resourceapi.ResourceClaim) (bool, error) {
			seen = append(seen, claim.ResourceVersion)
			return upsert(ownDevice("dev-a", `{"vf":1}`))(claim)
		}

		Expect(update(context.Background(), fake, mutate)).To(Succeed())
		Expect(seen).To(HaveLen(2), "the mutation runs once per attempt, on the claim fetched for that attempt")
	})

	It("stops instead of writing this driver's status onto a recreated claim", func() {
		// The claim this driver prepared for is gone and a different object now
		// holds the name, so the status it saved does not belong to what the
		// fetch returns.
		recreated := newClaim(foreignDevice("dev-foreign"))
		recreated.UID = k8stypes.UID("claim-a-uid-2")
		fake := k8sfake.NewSimpleClientset(recreated)

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if isStatusUpdate(action) {
				updateCalls++
			}
			return false, nil, nil
		})

		err := update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":2}`)))
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the claim this driver was updating is gone")
		Expect(updateCalls).To(BeZero(), "nothing should be written to the new claim")

		got := get(fake)
		Expect(deviceNamed(got.Status.Devices, consts.DriverName, "dev-a")).To(BeNil(), "the new claim must not carry this driver's status")
		Expect(deviceNamed(got.Status.Devices, "other.example.com", "dev-foreign")).NotTo(BeNil(), "the new claim's own status is untouched")
	})

	It("stops on a permanent fetch error instead of retrying to the timeout", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())

		getCalls := 0
		fake.PrependReactor("get", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			getCalls++
			return true, nil, apierrors.NewNotFound(gr, claimName)
		})

		err := update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(getCalls).To(Equal(1), "the fetch should not be retried once it fails permanently")
	})

	It("stops on a permanent update error instead of retrying to the timeout", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "ResourceClaim"}, claimName, nil)
		})

		err := update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(updateCalls).To(Equal(1), "an invalid update fails the same way every time, so it should not be retried")
	})

	It("stops when the mutation reports an error and returns it unchanged", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())
		errStale := errors.New("observation is stale")

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if isStatusUpdate(action) {
				updateCalls++
			}
			return false, nil, nil
		})

		err := update(context.Background(), fake, func(*resourceapi.ResourceClaim) (bool, error) { return false, errStale })
		Expect(errors.Is(err, errStale)).To(BeTrue(), "a mutation error is the caller's to interpret, so it must come back as is")
		Expect(updateCalls).To(BeZero())
	})

	It("stops on a mutation error that reads as a conflict", func() {
		// The conflict retry is for the API's optimistic concurrency. A mutation
		// is deterministic, so replaying one that failed only fails again.
		fake := k8sfake.NewSimpleClientset(newClaim())

		mutateCalls := 0
		mutate := func(_ *resourceapi.ResourceClaim) (bool, error) {
			mutateCalls++
			return false, apierrors.NewConflict(gr, claimName, errors.New("mutation raced"))
		}

		Expect(update(context.Background(), fake, mutate)).NotTo(Succeed())
		Expect(mutateCalls).To(Equal(1), "a failing mutation must not be replayed by the conflict retry")
	})

	It("returns the real error rather than the wait timeout when retries are exhausted", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())

		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			updateCalls++
			return true, nil, apierrors.NewInternalError(errors.New("etcd unavailable"))
		})

		err := update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInternalError(err)).To(BeTrue(), "the last real API error should be surfaced, not the generic wait timeout")
		// A transient error is retried to exhaustion, so every step is used.
		Expect(updateCalls).To(Equal(fastBackoff.Steps), "a retryable error should be retried until the backoff is exhausted")
	})

	It("recovers when a retryable error clears on a later attempt", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())

		getCalls := 0
		fake.PrependReactor("get", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			getCalls++
			if getCalls == 1 {
				return true, nil, apierrors.NewServerTimeout(gr, "get", 1)
			}
			return false, nil, nil
		})
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

		Expect(update(context.Background(), fake, upsert(ownDevice("dev-a", `{"vf":1}`)))).To(Succeed(), "a transient error that clears should let the update succeed")
		Expect(getCalls).To(Equal(3), "one failed fetch, then one fetch per update attempt")
		Expect(updateCalls).To(Equal(2), "the update should be retried once and then succeed")
	})

	It("surfaces context cancellation rather than the last API error", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if !isStatusUpdate(action) {
				return false, nil, nil
			}
			// Cancel while a real (retryable) API error is pending, so lastErr is set.
			cancel()
			return true, nil, apierrors.NewServerTimeout(gr, "update", 1)
		})

		err := update(ctx, fake, upsert(ownDevice("dev-a", `{"vf":1}`)))
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "a cancelled context is the reason the loop ended and should not be masked by lastErr")
	})

	It("reports a request cut short by the context as a failure, not a success", func() {
		// client-go returns the context's error when a request is cancelled
		// mid-flight. That has to come back as an error: a prepare-time write
		// cut off by its deadline is not a success.
		fake := k8sfake.NewSimpleClientset(newClaim())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake.PrependReactor("get", "resourceclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
			cancel()
			return true, nil, context.Canceled
		})

		err := update(ctx, fake, upsert(ownDevice("dev-a", `{"vf":1}`)))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "got %v", err)
	})

	It("refuses to run without the claim identity or a mutation", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())
		claims := fake.ResourceV1().ResourceClaims(namespace)
		mutate := upsert(ownDevice("dev-a", `{"vf":1}`))

		Expect(types.UpdateClaimStatusWithRetry(context.Background(), claims, "", claimUID, fastBackoff, mutate)).To(HaveOccurred())
		Expect(types.UpdateClaimStatusWithRetry(context.Background(), claims, claimName, "", fastBackoff, mutate)).To(HaveOccurred())
		Expect(types.UpdateClaimStatusWithRetry(context.Background(), claims, claimName, claimUID, fastBackoff, nil)).To(HaveOccurred())
	})
})
