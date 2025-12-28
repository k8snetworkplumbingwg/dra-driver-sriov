/*
Copyright 2025 The Kubernetes Authors.

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

//go:generate ../../bin/mockgen -destination mock/mock_cni.go -package mock -source interface.go

package cni

import (
	"context"

	"github.com/containerd/nri/pkg/api"
	resourcev1 "k8s.io/api/resource/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// Interface abstracts the CNI runtime to enable mocking in unit tests.
type Interface interface {
	AttachNetwork(ctx context.Context, pod *api.PodSandbox, podNetworkNamespace string, deviceConfig *types.PreparedDevice) (*resourcev1.NetworkDeviceData, map[string]interface{}, error)
	DetachNetwork(ctx context.Context, pod *api.PodSandbox, podNetworkNamespace string, deviceConfig *types.PreparedDevice) error
}

// Ensure Runtime implements Interface.
var _ Interface = (*Runtime)(nil)
