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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	resourceapi "k8s.io/api/resource/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

const (
	ownDriver     = "sriovnetwork.k8snetworkplumbingwg.io"
	foreignDriver = "gpu.example.com"
)

func status(driver, pool, device string) resourceapi.AllocatedDeviceStatus {
	return resourceapi.AllocatedDeviceStatus{Driver: driver, Pool: pool, Device: device}
}

func sharedStatus(driver, pool, device, shareID string) resourceapi.AllocatedDeviceStatus {
	s := status(driver, pool, device)
	id := shareID
	s.ShareID = &id
	return s
}

func identities(list []resourceapi.AllocatedDeviceStatus) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		id := s.Driver + "/" + s.Pool + "/" + s.Device
		if s.ShareID != nil {
			id += "#" + *s.ShareID
		}
		out = append(out, id)
	}
	return out
}

var _ = Describe("MergeDeviceStatuses", func() {
	It("keeps an entry another driver wrote during the conflict window", func() {
		// The claim we hold predates the conflict, so it has no knowledge of
		// the entry the other driver added. Restoring our snapshot wholesale is
		// what used to drop it.
		ours := []resourceapi.AllocatedDeviceStatus{status(ownDriver, "vf-pool", "vf0")}
		latest := []resourceapi.AllocatedDeviceStatus{
			status(ownDriver, "vf-pool", "vf0"),
			status(foreignDriver, "gpu-pool", "gpu0"),
		}

		merged := types.MergeDeviceStatuses(latest, ours, ownDriver)

		Expect(identities(merged)).To(ConsistOf(
			foreignDriver+"/gpu-pool/gpu0",
			ownDriver+"/vf-pool/vf0",
		))
	})

	It("keeps a foreign entry whose device name matches one of ours", func() {
		// Device names are unique only within a driver's pools, so ownership
		// has to be decided by the driver field rather than the name.
		ours := []resourceapi.AllocatedDeviceStatus{status(ownDriver, "vf-pool", "vf0")}
		latest := []resourceapi.AllocatedDeviceStatus{
			status(foreignDriver, "other-pool", "vf0"),
		}

		merged := types.MergeDeviceStatuses(latest, ours, ownDriver)

		Expect(identities(merged)).To(ConsistOf(
			foreignDriver+"/other-pool/vf0",
			ownDriver+"/vf-pool/vf0",
		))
	})

	It("replaces our own stale entry rather than duplicating it", func() {
		stale := status(ownDriver, "vf-pool", "vf0")
		stale.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth-old"}
		fresh := status(ownDriver, "vf-pool", "vf0")
		fresh.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth0"}

		merged := types.MergeDeviceStatuses(
			[]resourceapi.AllocatedDeviceStatus{stale},
			[]resourceapi.AllocatedDeviceStatus{fresh},
			ownDriver,
		)

		Expect(merged).To(HaveLen(1))
		Expect(merged[0].NetworkData).NotTo(BeNil())
		Expect(merged[0].NetworkData.InterfaceName).To(Equal("eth0"))
	})

	It("treats two shares of one device as separate entries", func() {
		ours := []resourceapi.AllocatedDeviceStatus{
			sharedStatus(ownDriver, "vf-pool", "vf0", "share-a"),
			sharedStatus(ownDriver, "vf-pool", "vf0", "share-b"),
		}

		merged := types.MergeDeviceStatuses(nil, ours, ownDriver)

		Expect(identities(merged)).To(ConsistOf(
			ownDriver+"/vf-pool/vf0#share-a",
			ownDriver+"/vf-pool/vf0#share-b",
		))
	})

	It("is stable when a retry changes nothing", func() {
		ours := []resourceapi.AllocatedDeviceStatus{
			status(ownDriver, "vf-pool", "vf0"),
			status(ownDriver, "vf-pool", "vf1"),
		}
		latest := append([]resourceapi.AllocatedDeviceStatus{
			status(foreignDriver, "gpu-pool", "gpu0"),
		}, ours...)

		first := types.MergeDeviceStatuses(latest, ours, ownDriver)
		second := types.MergeDeviceStatuses(first, ours, ownDriver)

		Expect(identities(second)).To(Equal(identities(first)))
	})

	It("keeps the last value when desired repeats a key", func() {
		// The prepare path now upserts, so desired should not carry duplicates;
		// if one ever slips through, the merge keeps the newest rather than the
		// stale first copy.
		stale := status(ownDriver, "vf-pool", "vf0")
		stale.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth-old"}
		fresh := status(ownDriver, "vf-pool", "vf0")
		fresh.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth0"}

		merged := types.MergeDeviceStatuses(nil, []resourceapi.AllocatedDeviceStatus{stale, fresh}, ownDriver)

		Expect(merged).To(HaveLen(1))
		Expect(merged[0].NetworkData.InterfaceName).To(Equal("eth0"))
	})
})

var _ = Describe("UpsertDeviceStatus", func() {
	It("appends a device that is not present yet", func() {
		list := []resourceapi.AllocatedDeviceStatus{status(ownDriver, "vf-pool", "vf0")}

		got := types.UpsertDeviceStatus(list, status(ownDriver, "vf-pool", "vf1"))

		Expect(identities(got)).To(Equal([]string{
			ownDriver + "/vf-pool/vf0",
			ownDriver + "/vf-pool/vf1",
		}))
	})

	It("replaces an existing entry in place instead of doubling it", func() {
		// The prepare path used to append unconditionally, so a claim already
		// carrying this device ended up with two entries the API server rejects.
		old := status(ownDriver, "vf-pool", "vf0")
		old.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth-old"}
		fresh := status(ownDriver, "vf-pool", "vf0")
		fresh.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth0"}

		got := types.UpsertDeviceStatus([]resourceapi.AllocatedDeviceStatus{old}, fresh)

		Expect(got).To(HaveLen(1))
		Expect(got[0].NetworkData.InterfaceName).To(Equal("eth0"))
	})

	It("keeps two shares of one device apart", func() {
		list := []resourceapi.AllocatedDeviceStatus{sharedStatus(ownDriver, "vf-pool", "vf0", "share-a")}

		got := types.UpsertDeviceStatus(list, sharedStatus(ownDriver, "vf-pool", "vf0", "share-b"))

		Expect(identities(got)).To(ConsistOf(
			ownDriver+"/vf-pool/vf0#share-a",
			ownDriver+"/vf-pool/vf0#share-b",
		))
	})
})
