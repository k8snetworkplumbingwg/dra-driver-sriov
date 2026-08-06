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
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
