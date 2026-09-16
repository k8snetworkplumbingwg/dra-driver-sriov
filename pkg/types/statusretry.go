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

package types

import (
	"context"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// conflictRetries is how many times a conflict is retried right away within
// one backoff step. A conflict means another writer got there first, and a
// refetch usually resolves it; the backoff is for transient errors.
const conflictRetries = 5

// IsPermanentStatusUpdateError reports whether a failed status update will keep
// failing the same way, so retrying the same request only burns the backoff and
// hides the real error. An invalid (for example a duplicate device key), forbidden
// or not-found response is permanent. Everything else, including conflicts (which
// the caller handles by refetching and merging) and transient failures such as
// server timeouts, throttling and network errors, is worth retrying, so callers
// treat a non-permanent error as retryable rather than enumerate every one.
func IsPermanentStatusUpdateError(err error) bool {
	return apierrors.IsInvalid(err) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsNotFound(err)
}

// ResourceClaimStatusClient is the subset of the generated ResourceClaim client
// that UpdateClaimStatusWithRetry needs. Both the typed client returned by
// ResourceV1().ResourceClaims(namespace) and the fake client used in tests
// satisfy it.
type ResourceClaimStatusClient interface {
	UpdateStatus(ctx context.Context, resourceClaim *resourceapi.ResourceClaim, opts metav1.UpdateOptions) (*resourceapi.ResourceClaim, error)
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*resourceapi.ResourceClaim, error)
}

// ClaimStatusMutation applies one caller's change to a freshly fetched claim
// and reports whether it changed anything. It runs once per attempt, so it must
// be deterministic and must not have side effects outside the claim. An error
// stops the retry and is returned to the caller as is.
type ClaimStatusMutation func(claim *resourceapi.ResourceClaim) (changed bool, err error)

// UpdateClaimStatusWithRetry fetches the claim, applies mutate and writes the
// status back, repeating all three on a conflict (right away, up to
// conflictRetries times per step) or a transient error (after a backoff step).
// Applying the mutation to the latest claim, rather than restoring a snapshot,
// keeps every entry the caller did not touch. It stops on a permanent error,
// on a claim recreated under the same name and on a mutation error, and once
// the backoff is exhausted returns the last API error rather than the timeout.
func UpdateClaimStatusWithRetry(
	ctx context.Context,
	claims ResourceClaimStatusClient,
	name string,
	uid k8stypes.UID,
	backoff wait.Backoff,
	mutate ClaimStatusMutation,
) error {
	if name == "" || uid == "" {
		return fmt.Errorf("claim status update needs the claim name and UID, got name %q and UID %q", name, uid)
	}
	if mutate == nil {
		return fmt.Errorf("claim status update for %s needs a mutation", name)
	}
	logger := klog.FromContext(ctx).WithName("UpdateClaimStatusWithRetry")

	var mutationErr error
	attempt := func(ctx context.Context) error {
		claim, err := claims.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		// A claim deleted and recreated under the same name is a different object,
		// and this driver's status belongs to the one that is gone.
		if claim.UID != uid {
			return fmt.Errorf("claim %s was replaced (UID %s is now %s): %w",
				name, uid, claim.UID,
				apierrors.NewNotFound(resourceapi.Resource("resourceclaims"), name))
		}

		changed, err := mutate(claim)
		if err != nil {
			mutationErr = err
			return err
		}
		if !changed {
			logger.V(2).Info("Claim status already up to date", "claim", uid)
			return nil
		}

		_, err = claims.UpdateStatus(ctx, claim, metav1.UpdateOptions{})
		return err
	}

	var lastErr error
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		var err error
		for i := 0; ; i++ {
			if err = attempt(ctx); !apierrors.IsConflict(err) || i == conflictRetries-1 {
				break
			}
		}
		if err == nil {
			return true, nil
		}
		// The request itself was cut short; there is nothing left to retry.
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		lastErr = err
		if mutationErr != nil || IsPermanentStatusUpdateError(err) {
			return false, err
		}
		logger.V(2).Info("Retrying claim status update", "claim", uid, "error", err.Error())
		return false, nil
	})

	// ExponentialBackoff reports its own timeout once the steps are exhausted;
	// surface the last real error instead so the caller can see what failed.
	// A context cancellation is itself the reason the loop ended (for example the
	// NRI runner shutting down), so leave it in place rather than masking it.
	if err != nil && lastErr != nil && ctx.Err() == nil {
		return lastErr
	}
	return err
}
