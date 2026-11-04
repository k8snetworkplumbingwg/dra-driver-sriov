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
	"errors"
	"fmt"
	"slices"

	resourceapi "k8s.io/api/resource/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
)

// ErrDeviceNotAllocated is returned by PatchDeviceNetworkStatuses for a device
// the claim's allocation does not list, or lists under several shares.
var ErrDeviceNotAllocated = errors.New("device is not allocated to the claim")

// deviceStatusKey identifies one entry of ResourceClaim.status.devices.
//
// AllocatedDeviceStatus documents a device as identified by its driver, pool
// and device name, and share ID distinguishes two allocations of one device
// when consumable capacity is in use. Device name alone is not enough: names
// are unique only within a driver's pools.
type deviceStatusKey struct {
	driver  string
	pool    string
	device  string
	shareID string
}

func keyOf(status resourceapi.AllocatedDeviceStatus) deviceStatusKey {
	key := deviceStatusKey{
		driver: status.Driver,
		pool:   status.Pool,
		device: status.Device,
	}
	if status.ShareID != nil {
		key.shareID = *status.ShareID
	}
	return key
}

// indexOf returns the position of the entry with key in list, or -1.
func indexOf(list []resourceapi.AllocatedDeviceStatus, key deviceStatusKey) int {
	return slices.IndexFunc(list, func(status resourceapi.AllocatedDeviceStatus) bool {
		return keyOf(status) == key
	})
}

// UpsertDeviceStatuses returns the prepare path's mutation: each status is
// recorded on the claim, replacing the entry of a device the claim already
// carries. A prepared device starts over from its configuration, so an entry
// left by an earlier use of the claim is replaced whole rather than patched.
// Every other entry, this driver's or another's, is left as it is. The list
// never ends up with two entries of one key, which the API server rejects.
func UpsertDeviceStatuses(statuses []resourceapi.AllocatedDeviceStatus) ClaimStatusMutation {
	return func(claim *resourceapi.ResourceClaim) (bool, error) {
		changed := false
		for _, status := range statuses {
			switch i := indexOf(claim.Status.Devices, keyOf(status)); {
			case i < 0:
				claim.Status.Devices = append(claim.Status.Devices, status)
			case apiequality.Semantic.DeepEqual(claim.Status.Devices[i], status):
				continue
			default:
				claim.Status.Devices[i] = status
			}
			changed = true
		}
		return changed, nil
	}
}

// DeviceNetworkStatus is what the NRI path records for one device once its
// network is attached.
type DeviceNetworkStatus struct {
	// Driver, Pool and Device identify the allocated device. ShareID may be
	// nil: the allocation supplies it when the device is allocated once.
	Driver  string
	Pool    string
	Device  string
	ShareID *string
	// NetworkData replaces the entry's NetworkData; nil clears it.
	NetworkData *resourceapi.NetworkDeviceData
	// Data replaces the entry's Data when set.
	Data *runtime.RawExtension
}

// allocatedKey resolves update to the key of the allocated device it names.
// The share ID comes from the allocation when update carries none, so the entry
// of a shared device is found under its real key. A device the allocation does
// not list, or lists under several shares update cannot tell apart, has no key.
func allocatedKey(claim *resourceapi.ResourceClaim, update DeviceNetworkStatus) (deviceStatusKey, bool) {
	if claim.Status.Allocation == nil {
		return deviceStatusKey{}, false
	}
	var key deviceStatusKey
	matches := 0
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != update.Driver || result.Pool != update.Pool || result.Device != update.Device {
			continue
		}
		if update.ShareID != nil && (result.ShareID == nil || string(*result.ShareID) != *update.ShareID) {
			continue
		}
		key = deviceStatusKey{driver: result.Driver, pool: result.Pool, device: result.Device}
		if result.ShareID != nil {
			key.shareID = string(*result.ShareID)
		}
		matches++
	}
	return key, matches == 1
}

// PatchDeviceNetworkStatuses returns the NRI path's mutation: the network data
// of each device is set on this driver's entry for it, nothing else on the
// claim is touched. A device without an entry gets one, which makes good a
// status write that failed during prepare. A device the allocation no longer
// lists stops the mutation with ErrDeviceNotAllocated; the API server would
// reject its entry anyway.
func PatchDeviceNetworkStatuses(updates []DeviceNetworkStatus) ClaimStatusMutation {
	return func(claim *resourceapi.ResourceClaim) (bool, error) {
		changed := false
		for _, update := range updates {
			key, allocated := allocatedKey(claim, update)
			if !allocated {
				return false, fmt.Errorf("device %s/%s: %w", update.Pool, update.Device, ErrDeviceNotAllocated)
			}
			i := indexOf(claim.Status.Devices, key)
			if i < 0 {
				entry := resourceapi.AllocatedDeviceStatus{Driver: key.driver, Pool: key.pool, Device: key.device}
				if key.shareID != "" {
					entry.ShareID = &key.shareID
				}
				claim.Status.Devices = append(claim.Status.Devices, entry)
				i = len(claim.Status.Devices) - 1
				changed = true
			}
			entry := &claim.Status.Devices[i]
			if !apiequality.Semantic.DeepEqual(entry.NetworkData, update.NetworkData) {
				entry.NetworkData = update.NetworkData
				changed = true
			}
			if update.Data != nil && !apiequality.Semantic.DeepEqual(entry.Data, update.Data) {
				entry.Data = update.Data
				changed = true
			}
		}
		return changed, nil
	}
}
