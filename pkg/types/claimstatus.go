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
)

// ErrDeviceNotAllocated means the claim's allocation does not list the device exactly once.
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

// UpsertDeviceStatuses returns the prepare path's mutation: each status replaces
// the whole entry with its key, since a prepared device starts over, or is
// appended. Other entries stay as they are, and no key appears twice.
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

// allocatedKey returns the key of the allocated device update names, with the
// share ID from the allocation. It reports false unless exactly one result matches.
func allocatedKey(claim *resourceapi.ResourceClaim, update resourceapi.AllocatedDeviceStatus) (deviceStatusKey, bool) {
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

// PatchDeviceNetworkStatuses returns the NRI path's mutation: it sets NetworkData
// (nil clears it) and, when set, Data on this driver's entry for each device,
// adding the entry if prepare's write was lost. A device the allocation does
// not list stops it with ErrDeviceNotAllocated.
func PatchDeviceNetworkStatuses(updates []resourceapi.AllocatedDeviceStatus) ClaimStatusMutation {
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
