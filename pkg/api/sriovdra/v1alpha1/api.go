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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//nolint:gochecknoinits // Required for Kubernetes scheme registration
func init() {
	SchemeBuilder.Register(&SriovResourceFilter{}, &SriovResourceFilterList{})
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SriovResourceFilter is a filter for SR-IOV resources
type SriovResourceFilter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SriovResourceFilterSpec `json:"spec"`
}

// SriovResourceFilterSpec is the spec for a SriovResourceFilter
type SriovResourceFilterSpec struct {
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	Configs      []Config          `json:"configs,omitempty"`
}

type Config struct {
	ResourceName    string           `json:"resourceName,omitempty"`
	ResourceFilters []ResourceFilter `json:"resourceFilters,omitempty"`
}

// ResourceFilter is a filter for a resource
type ResourceFilter struct {
	Vendors      []string `json:"vendors,omitempty"`
	Devices      []string `json:"devices,omitempty"`
	PciAddresses []string `json:"pciAddresses,omitempty"`
	PfNames      []string `json:"pfNames,omitempty"`
	RootDevices  []string `json:"rootDevices,omitempty"`
	Drivers      []string `json:"drivers,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SriovResourceFilterList contains a list of SriovResourceFilter
type SriovResourceFilterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SriovResourceFilter `json:"items"`
}
