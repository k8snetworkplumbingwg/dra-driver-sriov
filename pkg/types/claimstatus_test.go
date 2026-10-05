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
	"fmt"
	"math/rand/v2"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

const (
	ownDriver     = "sriovnetwork.k8snetworkplumbingwg.io"
	foreignDriver = "gpu.example.com"
)

func status(driver, pool, device string) resourceapi.AllocatedDeviceStatus {
	return resourceapi.AllocatedDeviceStatus{Driver: driver, Pool: pool, Device: device}
}

func statusWithData(driver, pool, device, data string) resourceapi.AllocatedDeviceStatus {
	s := status(driver, pool, device)
	s.Data = &runtime.RawExtension{Raw: []byte(data)}
	return s
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

func claimWith(devices ...resourceapi.AllocatedDeviceStatus) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{Status: resourceapi.ResourceClaimStatus{Devices: devices}}
}

func data(list []resourceapi.AllocatedDeviceStatus, device string) string {
	for _, s := range list {
		if s.Driver == ownDriver && s.Device == device && s.Data != nil {
			return string(s.Data.Raw)
		}
	}
	return ""
}

var _ = Describe("UpsertDeviceStatuses", func() {
	It("keeps an entry another driver wrote during the conflict window", func() {
		// The latest claim carries an entry this driver never saw; the mutation
		// only touches its own prepared device, so the entry stays.
		claim := claimWith(status(foreignDriver, "gpu-pool", "gpu0"))

		changed, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{status(ownDriver, "vf-pool", "vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(identities(claim.Status.Devices)).To(ConsistOf(
			foreignDriver+"/gpu-pool/gpu0",
			ownDriver+"/vf-pool/vf0",
		))
	})

	It("keeps this driver's other entries written during the conflict window", func() {
		// Restoring a pre-conflict snapshot of this driver's entries used to drop
		// vf2, added by the other status writer, and revert vf1 to a stale value.
		claim := claimWith(
			statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`),
			statusWithData(ownDriver, "vf-pool", "vf1", `{"v":99}`),
			statusWithData(ownDriver, "vf-pool", "vf2", `{"v":3}`),
		)

		changed, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{statusWithData(ownDriver, "vf-pool", "vf0", `{"v":2}`)})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(identities(claim.Status.Devices)).To(ConsistOf(
			ownDriver+"/vf-pool/vf0",
			ownDriver+"/vf-pool/vf1",
			ownDriver+"/vf-pool/vf2",
		))
		Expect(data(claim.Status.Devices, "vf0")).To(Equal(`{"v":2}`))
		Expect(data(claim.Status.Devices, "vf1")).To(Equal(`{"v":99}`), "an untouched own entry must not be reverted")
		Expect(data(claim.Status.Devices, "vf2")).To(Equal(`{"v":3}`), "a same-driver entry added concurrently must survive")
	})

	It("keeps a foreign entry whose device name matches one of ours", func() {
		// Device names are unique only within a driver's pools, so the key
		// includes the driver rather than the name alone.
		claim := claimWith(status(foreignDriver, "other-pool", "vf0"))

		_, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{status(ownDriver, "vf-pool", "vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())

		Expect(identities(claim.Status.Devices)).To(ConsistOf(
			foreignDriver+"/other-pool/vf0",
			ownDriver+"/vf-pool/vf0",
		))
	})

	It("replaces the whole entry of a device the claim already carries", func() {
		// A prepared device starts over: network data from an earlier use of the
		// claim describes an attachment that no longer exists.
		stale := statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`)
		stale.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth-old"}
		claim := claimWith(stale)

		changed, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{statusWithData(ownDriver, "vf-pool", "vf0", `{"v":2}`)})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(claim.Status.Devices).To(HaveLen(1))
		Expect(string(claim.Status.Devices[0].Data.Raw)).To(Equal(`{"v":2}`))
		Expect(claim.Status.Devices[0].NetworkData).To(BeNil())
	})

	It("reports no change when the claim already holds the same entries", func() {
		// A retry after an update whose response was lost must not write again.
		claim := claimWith(
			status(foreignDriver, "gpu-pool", "gpu0"),
			statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`),
		)
		before := claim.DeepCopy()

		changed, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`)})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(claim).To(Equal(before))
	})

	It("treats two shares of one device as separate entries", func() {
		claim := claimWith(sharedStatus(ownDriver, "vf-pool", "vf0", "share-a"))

		_, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{sharedStatus(ownDriver, "vf-pool", "vf0", "share-b")})(claim)
		Expect(err).NotTo(HaveOccurred())

		Expect(identities(claim.Status.Devices)).To(ConsistOf(
			ownDriver+"/vf-pool/vf0#share-a",
			ownDriver+"/vf-pool/vf0#share-b",
		))
	})

	It("never leaves two entries with the same key", func() {
		// The API server rejects a duplicate key, so a repeated key in the
		// prepared list keeps the last value rather than doubling the entry.
		claim := claimWith()

		_, err := types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{
			statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`),
			statusWithData(ownDriver, "vf-pool", "vf0", `{"v":2}`),
		})(claim)
		Expect(err).NotTo(HaveOccurred())

		Expect(claim.Status.Devices).To(HaveLen(1))
		Expect(data(claim.Status.Devices, "vf0")).To(Equal(`{"v":2}`))
	})
})

var _ = Describe("PatchDeviceNetworkStatuses", func() {
	networkData := &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}}
	update := func(device string) types.DeviceNetworkStatus {
		return types.DeviceNetworkStatus{
			Driver:      ownDriver,
			Pool:        "vf-pool",
			Device:      device,
			NetworkData: networkData,
			Data:        &runtime.RawExtension{Raw: []byte(`{"vfConfig":{},"cniResult":{"ok":true}}`)},
		}
	}
	allocated := func(driver, pool, device string) resourceapi.DeviceRequestAllocationResult {
		return resourceapi.DeviceRequestAllocationResult{Request: "req", Driver: driver, Pool: pool, Device: device}
	}
	sharedAllocation := func(driver, pool, device, shareID string) resourceapi.DeviceRequestAllocationResult {
		r := allocated(driver, pool, device)
		id := k8stypes.UID(shareID)
		r.ShareID = &id
		return r
	}
	// allocatedClaim is a claim whose allocation lists results and whose
	// status carries devices, as the API server would hold it.
	allocatedClaim := func(results []resourceapi.DeviceRequestAllocationResult, devices ...resourceapi.AllocatedDeviceStatus) *resourceapi.ResourceClaim {
		claim := claimWith(devices...)
		claim.Status.Allocation = &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: results}}
		return claim
	}
	ownAllocation := []resourceapi.DeviceRequestAllocationResult{
		allocated(ownDriver, "vf-pool", "vf0"),
		allocated(ownDriver, "vf-pool", "vf1"),
		allocated(foreignDriver, "vf-pool", "vf0"),
	}

	It("sets the network data on this driver's entry only", func() {
		foreign := status(foreignDriver, "vf-pool", "vf0")
		other := statusWithData(ownDriver, "vf-pool", "vf1", `{"v":1}`)
		claim := allocatedClaim(ownAllocation, foreign, statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`), other)

		changed, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(claim.Status.Devices).To(HaveLen(3))
		Expect(claim.Status.Devices[0]).To(Equal(foreign), "a foreign entry with the same pool and device name is not ours")
		Expect(claim.Status.Devices[1].NetworkData).To(Equal(networkData))
		Expect(string(claim.Status.Devices[1].Data.Raw)).To(Equal(`{"vfConfig":{},"cniResult":{"ok":true}}`))
		Expect(claim.Status.Devices[2]).To(Equal(other), "an own entry that was not updated stays as it is")
	})

	It("leaves conditions and unrelated fields of the entry alone", func() {
		entry := statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`)
		entry.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Attached"}}
		claim := allocatedClaim(ownAllocation, entry)

		_, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())

		Expect(claim.Status.Devices[0].Conditions).To(Equal(entry.Conditions))
	})

	It("adds the entry of an allocated device that has none", func() {
		// The status write during prepare failed, so the claim carries no entry
		// for vf0 even though it is allocated; the network data completes it.
		claim := allocatedClaim(ownAllocation, statusWithData(ownDriver, "vf-pool", "vf1", `{"v":1}`))

		changed, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(identities(claim.Status.Devices)).To(Equal([]string{ownDriver + "/vf-pool/vf1", ownDriver + "/vf-pool/vf0"}))
		Expect(claim.Status.Devices[1].NetworkData).To(Equal(networkData))
		Expect(string(claim.Status.Devices[1].Data.Raw)).To(Equal(`{"vfConfig":{},"cniResult":{"ok":true}}`))
	})

	It("stops with ErrDeviceNotAllocated for a device the allocation no longer lists", func() {
		// The API server rejects a status entry for a device that is not
		// allocated, so nothing is written and the caller gets told why.
		claim := allocatedClaim([]resourceapi.DeviceRequestAllocationResult{allocated(ownDriver, "vf-pool", "vf1")}, statusWithData(ownDriver, "vf-pool", "vf1", `{"v":1}`))
		before := claim.DeepCopy()

		_, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(errors.Is(err, types.ErrDeviceNotAllocated)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("vf-pool/vf0"))
		Expect(claim).To(Equal(before))
	})

	It("stops with ErrDeviceNotAllocated on a deallocated claim", func() {
		claim := claimWith(statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`))
		before := claim.DeepCopy()

		_, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(errors.Is(err, types.ErrDeviceNotAllocated)).To(BeTrue(), "got %v", err)
		Expect(claim).To(Equal(before))
	})

	It("reports no change when the entry already holds the network data", func() {
		claim := allocatedClaim(ownAllocation, statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`))
		Expect(types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)).To(BeTrue())
		before := claim.DeepCopy()

		// The same update built again, as a retry would.
		changed, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(claim).To(Equal(before))
	})

	It("clears the network data when the update carries none", func() {
		entry := statusWithData(ownDriver, "vf-pool", "vf0", `{"v":1}`)
		entry.NetworkData = &resourceapi.NetworkDeviceData{InterfaceName: "eth-old"}
		claim := allocatedClaim(ownAllocation, entry)

		changed, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{{Driver: ownDriver, Pool: "vf-pool", Device: "vf0"}})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(claim.Status.Devices[0].NetworkData).To(BeNil())
		Expect(string(claim.Status.Devices[0].Data.Raw)).To(Equal(`{"v":1}`), "Data is left as it is when the update has none")
	})

	It("finds the entry of a shared device through the allocation", func() {
		// The update names the device without a share ID, as the NRI path does;
		// the allocation says which share it is.
		claim := allocatedClaim(
			[]resourceapi.DeviceRequestAllocationResult{sharedAllocation(ownDriver, "vf-pool", "vf0", "share-a")},
			sharedStatus(ownDriver, "vf-pool", "vf0", "share-a"),
		)

		changed, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		Expect(claim.Status.Devices).To(HaveLen(1))
		Expect(claim.Status.Devices[0].NetworkData).To(Equal(networkData))
	})

	It("adds the entry of a shared device under its share ID", func() {
		claim := allocatedClaim([]resourceapi.DeviceRequestAllocationResult{sharedAllocation(ownDriver, "vf-pool", "vf0", "share-a")})

		_, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(err).NotTo(HaveOccurred())

		Expect(identities(claim.Status.Devices)).To(Equal([]string{ownDriver + "/vf-pool/vf0#share-a"}))
	})

	It("keeps two shares of one device apart", func() {
		a := sharedStatus(ownDriver, "vf-pool", "vf0", "share-a")
		b := sharedStatus(ownDriver, "vf-pool", "vf0", "share-b")
		twoShares := []resourceapi.DeviceRequestAllocationResult{
			sharedAllocation(ownDriver, "vf-pool", "vf0", "share-a"),
			sharedAllocation(ownDriver, "vf-pool", "vf0", "share-b"),
		}
		claim := allocatedClaim(twoShares, a, b)

		// Without a share ID the update cannot tell the shares apart, so
		// neither may be touched.
		_, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{update("vf0")})(claim)
		Expect(errors.Is(err, types.ErrDeviceNotAllocated)).To(BeTrue(), "got %v", err)
		Expect(claim.Status.Devices).To(Equal([]resourceapi.AllocatedDeviceStatus{a, b}))

		shareB := "share-b"
		changed, err := types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{{Driver: ownDriver, Pool: "vf-pool", Device: "vf0", ShareID: &shareB, NetworkData: networkData}})(claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(claim.Status.Devices[0]).To(Equal(a))
		Expect(claim.Status.Devices[1].NetworkData).To(Equal(networkData))
	})
})

var _ = Describe("status mutations interleaved at random", func() {
	// The prepare and NRI mutations run in any order and any number of times
	// against a claim that other writers change in between. Whatever the
	// interleaving, the claim never carries two entries with one key, an entry
	// this driver does not own is never touched, and every mutation is
	// idempotent, since the retry replays it on a claim it may already have
	// been applied to.
	It("keeps the claim free of duplicate keys and foreign entries intact", func() {
		const devices = 4
		seed := uint64(GinkgoRandomSeed())
		rng := rand.New(rand.NewPCG(seed, seed))

		results := make([]resourceapi.DeviceRequestAllocationResult, 0, devices+1)
		for i := range devices {
			results = append(results, resourceapi.DeviceRequestAllocationResult{Request: "req", Driver: ownDriver, Pool: "vf-pool", Device: fmt.Sprintf("vf%d", i)})
		}
		results = append(results, resourceapi.DeviceRequestAllocationResult{Request: "req", Driver: foreignDriver, Pool: "vf-pool", Device: "vf0"})
		foreign := statusWithData(foreignDriver, "vf-pool", "vf0", `{"foreign":true}`)
		claim := claimWith(foreign)
		claim.Status.Allocation = &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: results}}

		mutations := make([]types.ClaimStatusMutation, 0, 2*devices)
		for i := range devices {
			device := fmt.Sprintf("vf%d", i)
			mutations = append(mutations,
				types.UpsertDeviceStatuses([]resourceapi.AllocatedDeviceStatus{statusWithData(ownDriver, "vf-pool", device, fmt.Sprintf(`{"v":%d}`, i))}),
				types.PatchDeviceNetworkStatuses([]types.DeviceNetworkStatus{{
					Driver: ownDriver, Pool: "vf-pool", Device: device,
					NetworkData: &resourceapi.NetworkDeviceData{InterfaceName: "net" + device},
					Data:        &runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"vfConfig":{"v":%d},"cniResult":{}}`, i))},
				}}),
			)
		}

		for range 500 {
			mutate := mutations[rng.IntN(len(mutations))]
			_, err := mutate(claim)
			Expect(err).NotTo(HaveOccurred())
			changed, err := mutate(claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).To(BeFalse(), "a mutation applied twice in a row must report no change the second time (seed %d)", seed)

			seen := map[string]bool{}
			for _, id := range identities(claim.Status.Devices) {
				Expect(seen[id]).To(BeFalse(), "duplicate key %s (seed %d)", id, seed)
				seen[id] = true
			}
			Expect(claim.Status.Devices[0]).To(Equal(foreign), "foreign entry changed (seed %d)", seed)
			Expect(len(claim.Status.Devices)).To(BeNumerically("<=", devices+1))
		}
	})
})
