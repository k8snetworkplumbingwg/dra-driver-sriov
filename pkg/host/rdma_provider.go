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

package host

import (
	"github.com/Mellanox/rdmamap"
)

// RdmaProvider is a wrapper interface over rdmamap library
// This allows for easy mocking in unit tests
//
//go:generate mockgen -destination mock/mock_rdma_provider.go -source rdma_provider.go
type RdmaProvider interface {
	GetRdmaDevicesForPcidev(pciAddr string) []string
}

type defaultRdmaProvider struct{}

// GetRdmaDevicesForPcidev returns RDMA devices associated with a PCI device
func (defaultRdmaProvider) GetRdmaDevicesForPcidev(pciAddr string) []string {
	return rdmamap.GetRdmaDevicesForPcidev(pciAddr)
}

// newRdmaProvider creates a new default RDMA provider
func newRdmaProvider() RdmaProvider {
	return &defaultRdmaProvider{}
}
