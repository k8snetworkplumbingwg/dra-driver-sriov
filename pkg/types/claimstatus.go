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

// MergeDeviceStatuses returns latest with this driver's entries replaced by
// desired, preserving every entry owned by another driver.
//
// ResourceClaim.status.devices is shared by all the drivers that contributed a
// device to the claim. A driver that refetches the claim after an update
// conflict and restores its own pre-conflict copy of the list would undo the
// entry another driver wrote in the meantime, so the retry has to merge onto
// the latest list rather than replace it.
//
// Entries are ordered with the preserved foreign ones first, in the order they
// appear in latest, followed by desired in its own order, so a retry that
// changes nothing produces no diff.
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

	// Then this driver's own entries, deduplicated so a retry cannot append a
	// second copy of a device it already reported.
	seen := make(map[deviceStatusKey]struct{}, len(desired))
	for _, status := range desired {
		if status.Driver != driverName {
			continue
		}
		key := keyOf(status)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, status)
	}

	return merged
}
