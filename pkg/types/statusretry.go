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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

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
