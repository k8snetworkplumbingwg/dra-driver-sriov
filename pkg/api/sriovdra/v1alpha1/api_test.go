/*
 * Copyright 2025 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

var _ = Describe("NormalizeLinkType", func() {
	It("normalizes eth variants to ethernet", func() {
		Expect(NormalizeLinkType(consts.LinkTypeEth)).To(Equal(consts.LinkTypeEthernet))
		Expect(NormalizeLinkType("ETH")).To(Equal(consts.LinkTypeEthernet))
		Expect(NormalizeLinkType("Eth")).To(Equal(consts.LinkTypeEthernet))
		Expect(NormalizeLinkType(consts.LinkTypeEthernet)).To(Equal(consts.LinkTypeEthernet))
	})

	It("normalizes ib variants to infiniband", func() {
		Expect(NormalizeLinkType(consts.LinkTypeIB)).To(Equal(consts.LinkTypeInfiniband))
		Expect(NormalizeLinkType("IB")).To(Equal(consts.LinkTypeInfiniband))
		Expect(NormalizeLinkType("Ib")).To(Equal(consts.LinkTypeInfiniband))
		Expect(NormalizeLinkType(consts.LinkTypeInfiniband)).To(Equal(consts.LinkTypeInfiniband))
	})

	It("lowercases unknown values", func() {
		Expect(NormalizeLinkType("UNKNOWN")).To(Equal("unknown"))
	})
})
