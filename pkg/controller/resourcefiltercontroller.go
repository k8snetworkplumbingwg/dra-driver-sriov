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

package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	resourceapi "k8s.io/api/resource/v1"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
)

const (
	resourceFilterSyncEventName = "resource-filter-sync"
)

// SriovResourceFilterReconciler reconciles SriovResourceFilter objects
type SriovResourceFilterReconciler struct {
	client.Client
	nodeName              string
	namespace             string
	currentResourceFilter *sriovdrav1alpha1.SriovResourceFilter
	log                   klog.Logger
	deviceStateManager    devicestate.DeviceState
}

// NewSriovResourceFilterReconciler creates a new SriovResourceFilterReconciler
func NewSriovResourceFilterReconciler(client client.Client, nodeName, namespace string, deviceStateManager devicestate.DeviceState) *SriovResourceFilterReconciler {
	return &SriovResourceFilterReconciler{
		Client:             client,
		deviceStateManager: deviceStateManager,
		nodeName:           nodeName,
		namespace:          namespace,
		log:                klog.Background().WithName("SriovResourceFilter"),
	}
}

// Reconcile handles the reconciliation of SriovResourceFilter resources
func (r *SriovResourceFilterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.Info("Starting reconcile", "request", req.NamespacedName, "watchedNamespace", r.namespace)

	// Get the current node to check its labels
	node := &metav1.PartialObjectMetadata{}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	if err := r.Get(ctx, types.NamespacedName{Name: r.nodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Error(err, "Node not found", "nodeName", r.nodeName)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.log.Error(err, "Failed to get node", "nodeName", r.nodeName)
		return ctrl.Result{}, err
	}

	// List all SriovResourceFilter objects in the operator namespace
	resourceFilterList := &sriovdrav1alpha1.SriovResourceFilterList{}
	if err := r.List(ctx, resourceFilterList, client.InNamespace(r.namespace)); err != nil {
		r.log.Error(err, "Failed to list SriovResourceFilter objects", "namespace", r.namespace)
		return ctrl.Result{}, err
	}

	// Find matching resource filters for this node
	var matchingFilters []*sriovdrav1alpha1.SriovResourceFilter
	for i := range resourceFilterList.Items {
		filter := &resourceFilterList.Items[i]
		if r.matchesNodeSelector(node.Labels, filter.Spec.NodeSelector) {
			matchingFilters = append(matchingFilters, filter)
		}
	}

	// Handle the results
	switch len(matchingFilters) {
	case 0:
		r.log.Info("No matching SriovResourceFilter found for node", "nodeName", r.nodeName)
		r.currentResourceFilter = nil
		// Clear resource filter from devices since no filter matches
		if err := r.applyResourceFilterToDevices(ctx); err != nil {
			r.log.Error(err, "Failed to clear resource filter from devices")
			return ctrl.Result{}, err
		}
	case 1:
		r.log.Info("Found matching SriovResourceFilter for node", "nodeName", r.nodeName, "filter", matchingFilters[0].Name)
		r.currentResourceFilter = matchingFilters[0]
		// Apply resource filter to devices
		if err := r.applyResourceFilterToDevices(ctx); err != nil {
			r.log.Error(err, "Failed to apply resource filter to devices")
			return ctrl.Result{}, err
		}
	default:
		// Multiple matches - log error and don't use any
		filterNames := make([]string, len(matchingFilters))
		for i, filter := range matchingFilters {
			filterNames[i] = filter.Name
		}
		r.log.Error(fmt.Errorf("multiple SriovResourceFilter objects match node"),
			"Multiple resource filters match node, ignoring all",
			"nodeName", r.nodeName,
			"matchingFilters", filterNames)
		r.currentResourceFilter = nil
	}

	return ctrl.Result{}, nil
}

// GetCurrentResourceFilter returns the currently active SriovResourceFilter for the node
func (r *SriovResourceFilterReconciler) GetCurrentResourceFilter() *sriovdrav1alpha1.SriovResourceFilter {
	return r.currentResourceFilter
}

// HasResourceFilter returns true if there is currently an active SriovResourceFilter for the node
func (r *SriovResourceFilterReconciler) HasResourceFilter() bool {
	return r.currentResourceFilter != nil
}

// GetConfigs returns the configs from the currently active SriovResourceFilter
// Returns nil if no resource filter is active
func (r *SriovResourceFilterReconciler) GetConfigs() []sriovdrav1alpha1.Config {
	if r.currentResourceFilter == nil {
		return nil
	}
	return r.currentResourceFilter.Spec.Configs
}

// GetResourceFilters returns all resource filters from all configs in the currently active SriovResourceFilter
// Returns nil if no resource filter is active
//
// Deprecated: Use GetConfigs() instead for better resource name handling
func (r *SriovResourceFilterReconciler) GetResourceFilters() []sriovdrav1alpha1.ResourceFilter {
	if r.currentResourceFilter == nil {
		return nil
	}
	var allFilters []sriovdrav1alpha1.ResourceFilter
	for _, config := range r.currentResourceFilter.Spec.Configs {
		allFilters = append(allFilters, config.ResourceFilters...)
	}
	return allFilters
}

// GetResourceNames returns all resource names from the currently active SriovResourceFilter
// Returns nil if no resource filter is active
func (r *SriovResourceFilterReconciler) GetResourceNames() []string {
	if r.currentResourceFilter == nil {
		return nil
	}
	var resourceNames []string
	for _, config := range r.currentResourceFilter.Spec.Configs {
		if config.ResourceName != "" {
			resourceNames = append(resourceNames, config.ResourceName)
		}
	}
	return resourceNames
}

// matchesNodeSelector checks if node labels match the given selector
func (r *SriovResourceFilterReconciler) matchesNodeSelector(nodeLabels map[string]string, nodeSelector map[string]string) bool {
	if len(nodeSelector) == 0 {
		// Empty selector matches all nodes
		return true
	}

	selector := labels.Set(nodeSelector).AsSelector()
	return selector.Matches(labels.Set(nodeLabels))
}

// applyResourceFilterToDevices applies the current resource filter to devices
func (r *SriovResourceFilterReconciler) applyResourceFilterToDevices(ctx context.Context) error {
	deviceResourceMap := r.getFilteredDeviceResourceMap()
	return r.deviceStateManager.UpdateDeviceResourceNames(ctx, deviceResourceMap)
}

// getFilteredDeviceResourceMap returns a map of device name to resource name based on the current resource filter
func (r *SriovResourceFilterReconciler) getFilteredDeviceResourceMap() map[string]string {
	deviceResourceMap := make(map[string]string)

	// If no resource filter is active, return empty map (clears resource names)
	if r.currentResourceFilter == nil {
		r.log.V(2).Info("No active resource filter, clearing all resource names")
		return deviceResourceMap
	}

	// Get all allocatable devices from device state manager
	allocatableDevices := r.deviceStateManager.GetAllocatableDevices()

	r.log.V(2).Info("Applying resource filter to devices",
		"filterName", r.currentResourceFilter.Name,
		"totalConfigs", len(r.currentResourceFilter.Spec.Configs),
		"totalDevices", len(allocatableDevices))

	// Iterate through each config and apply its resource filters to devices
	for _, config := range r.currentResourceFilter.Spec.Configs {
		if config.ResourceName == "" {
			r.log.V(2).Info("Skipping config with empty resource name", "filterName", r.currentResourceFilter.Name)
			continue
		}

		r.log.V(3).Info("Processing config",
			"filterName", r.currentResourceFilter.Name,
			"resourceName", config.ResourceName,
			"filtersCount", len(config.ResourceFilters))

		// Apply this config's resource filters to devices
		for deviceName, device := range allocatableDevices {
			// Skip device if it's already assigned a resource name
			if _, exists := deviceResourceMap[deviceName]; exists {
				continue
			}

			if r.deviceMatchesFilters(device, config.ResourceFilters) {
				deviceResourceMap[deviceName] = config.ResourceName
				r.log.V(3).Info("Device matches config filter",
					"deviceName", deviceName,
					"resourceName", config.ResourceName,
					"filterName", r.currentResourceFilter.Name)
			}
		}
	}

	r.log.Info("Resource filter applied",
		"filterName", r.currentResourceFilter.Name,
		"matchingDevices", len(deviceResourceMap),
		"totalDevices", len(allocatableDevices))

	return deviceResourceMap
}

// deviceMatchesFilters checks if a device matches any of the provided resource filters
func (r *SriovResourceFilterReconciler) deviceMatchesFilters(device resourceapi.Device, filters []sriovdrav1alpha1.ResourceFilter) bool {
	// If no filters are specified, match all devices
	if len(filters) == 0 {
		return true
	}

	// Device matches if it matches ANY of the filters (OR logic)
	for _, filter := range filters {
		if r.deviceMatchesFilter(device, filter) {
			return true
		}
	}

	return false
}

// deviceMatchesFilter checks if a device matches a specific resource filter
func (r *SriovResourceFilterReconciler) deviceMatchesFilter(device resourceapi.Device, filter sriovdrav1alpha1.ResourceFilter) bool {
	// Check vendor IDs
	if len(filter.Vendors) > 0 {
		vendorAttr, exists := device.Attributes[consts.AttributeVendorID]
		if !exists || vendorAttr.StringValue == nil {
			return false
		}
		if !r.stringSliceContains(filter.Vendors, *vendorAttr.StringValue) {
			return false
		}
	}

	// Check device IDs
	if len(filter.Devices) > 0 {
		deviceAttr, exists := device.Attributes[consts.AttributeDeviceID]
		if !exists || deviceAttr.StringValue == nil {
			return false
		}
		if !r.stringSliceContains(filter.Devices, *deviceAttr.StringValue) {
			return false
		}
	}

	// Check PCI addresses
	if len(filter.PciAddresses) > 0 {
		pciAttr, exists := device.Attributes[consts.AttributePciAddress]
		if !exists || pciAttr.StringValue == nil {
			return false
		}
		if !r.stringSliceContains(filter.PciAddresses, *pciAttr.StringValue) {
			return false
		}
	}

	// Check PF names
	if len(filter.PfNames) > 0 {
		pfAttr, exists := device.Attributes[consts.AttributePFName]
		if !exists || pfAttr.StringValue == nil {
			return false
		}
		if !r.stringSliceContains(filter.PfNames, *pfAttr.StringValue) {
			return false
		}
	}

	// Check root devices (parent PCI addresses, e.g., "0000:00:00.0")
	// This filters by immediate parent device for granular filtering
	if len(filter.RootDevices) > 0 {
		parentAttr, exists := device.Attributes[consts.AttributeParentPciAddress]
		if !exists || parentAttr.StringValue == nil {
			return false
		}
		if !r.stringSliceContains(filter.RootDevices, *parentAttr.StringValue) {
			return false
		}
	}

	// Check NUMA nodes
	if len(filter.NumaNodes) > 0 {
		numaAttr, exists := device.Attributes[consts.AttributeNumaNode]
		if !exists || numaAttr.IntValue == nil {
			return false
		}
		numaStr := strconv.FormatInt(*numaAttr.IntValue, 10)
		if !r.stringSliceContains(filter.NumaNodes, numaStr) {
			return false
		}
	}

	// Check drivers - this is more complex as we need to check the current driver binding
	// For now, we'll skip this check as it would require additional system calls
	// TODO: Implement driver checking if needed
	if len(filter.Drivers) > 0 {
		r.log.V(3).Info("Driver filtering not yet implemented", "deviceName", device.Name)
	}

	// All specified filters match
	return true
}

// stringSliceContains checks if a slice contains a specific string
func (r *SriovResourceFilterReconciler) stringSliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovResourceFilterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	qHandler := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: r.namespace,
			Name:      resourceFilterSyncEventName,
		}}, time.Second)
	}

	delayedEventHandler := handler.Funcs{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			r.log.Info("Enqueuing sync for create event",
				"resource", e.Object.GetName(),
				"type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			r.log.Info("Enqueuing sync for update event",
				"resource", e.ObjectNew.GetName(),
				"type", e.ObjectNew.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			r.log.Info("Enqueuing sync for delete event",
				"resource", e.Object.GetName(),
				"type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			r.log.Info("Enqueuing sync for generic event",
				"resource", e.Object.GetName(),
				"type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
	}

	// Node event handler - we care about node label changes
	nodeEventHandler := handler.Funcs{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// Only care about our node
			if e.Object.GetName() == r.nodeName {
				r.log.Info("Enqueuing sync for node create event", "node", e.Object.GetName())
				qHandler(w)
			}
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// Only care about our node and only if labels changed
			if e.ObjectNew.GetName() == r.nodeName {
				oldLabels := e.ObjectOld.GetLabels()
				newLabels := e.ObjectNew.GetLabels()
				if !labels.Equals(oldLabels, newLabels) {
					r.log.Info("Enqueuing sync for node label change event", "node", e.ObjectNew.GetName())
					qHandler(w)
				}
			}
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// Only care about our node
			if e.Object.GetName() == r.nodeName {
				r.log.Info("Enqueuing sync for node delete event", "node", e.Object.GetName())
				qHandler(w)
			}
		},
	}

	// Send initial sync event to trigger reconcile when controller is started
	var eventChan = make(chan event.GenericEvent, 1)
	eventChan <- event.GenericEvent{Object: &sriovdrav1alpha1.SriovResourceFilter{
		ObjectMeta: metav1.ObjectMeta{Name: resourceFilterSyncEventName, Namespace: r.namespace}}}
	close(eventChan)

	// Create predicate to filter SriovResourceFilter events to only the operator namespace
	namespacePredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == r.namespace
	})

	// Set up PartialObjectMetadata for Node resources
	nodeMetadata := &metav1.PartialObjectMetadata{}
	nodeMetadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovdrav1alpha1.SriovResourceFilter{}).
		Watches(nodeMetadata, nodeEventHandler).
		Watches(&sriovdrav1alpha1.SriovResourceFilter{}, delayedEventHandler).
		WithEventFilter(namespacePredicate).
		WatchesRawSource(source.Channel(eventChan, &handler.EnqueueRequestForObject{})).
		Complete(r)
}
