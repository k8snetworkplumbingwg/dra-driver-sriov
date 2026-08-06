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
	resourceapi "k8s.io/api/resource/v1"
)

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

// UpsertDeviceStatus replaces the entry in list whose key (driver, pool, device
// and share ID) matches status, or appends status when none matches. It keeps
// status.devices free of duplicate keys: on the API server the list is a
// listType=map, so a second entry with the same key is rejected as invalid
// rather than merged. Callers that build status by appending should upsert so a
// device already reported on the claim is updated in place instead of doubled.
func UpsertDeviceStatus(
	list []resourceapi.AllocatedDeviceStatus,
	status resourceapi.AllocatedDeviceStatus,
) []resourceapi.AllocatedDeviceStatus {
	key := keyOf(status)
	for i := range list {
		if keyOf(list[i]) == key {
			list[i] = status
			return list
		}
	}
	return append(list, status)
}

// MergeDeviceStatuses returns latest with this driver's entries replaced by
// desired, preserving every entry owned by another driver.
//
// ResourceClaim.status.devices is shared by all the drivers that contributed a
// device to the claim. A driver that refetches the claim after an update
// conflict and restores its own pre-conflict copy of the list would undo the
// entry another driver wrote in the meantime, so the retry has to merge onto
// the latest list rather than replace it.
//
// Foreign entries keep their original order and this driver's entries follow.
// status.devices is a listType=map keyed by driver, pool, device and share ID,
// so the API server treats it as an unordered set and the position of an entry
// carries no meaning of its own.
func MergeDeviceStatuses(
	latest []resourceapi.AllocatedDeviceStatus,
	desired []resourceapi.AllocatedDeviceStatus,
	driverName string,
) []resourceapi.AllocatedDeviceStatus {
	merged := make([]resourceapi.AllocatedDeviceStatus, 0, len(latest)+len(desired))

	// Everything this driver does not own is carried over untouched. Ownership
	// is read from the entry itself rather than inferred from the pool, because
	// pool names are chosen per driver.
	for _, status := range latest {
		if status.Driver == driverName {
			continue
		}
		merged = append(merged, status)
	}

	// Then this driver's own entries. Upsert rather than append so a repeated key
	// keeps the last value, and the result never carries two entries with the same
	// key, which the API server would reject.
	for _, status := range desired {
		if status.Driver != driverName {
			continue
		}
		merged = UpsertDeviceStatus(merged, status)
	}

	return merged
}
