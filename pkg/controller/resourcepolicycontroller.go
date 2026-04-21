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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
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

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
)

const (
	resourcePolicySyncEventName = "resource-policy-sync"
)

// SriovResourcePolicyReconciler reconciles SriovResourcePolicy and DeviceAttributes objects
type SriovResourcePolicyReconciler struct {
	client.Client
	nodeName           string
	namespace          string
	log                klog.Logger
	deviceStateManager devicestate.DeviceState
}

// NewSriovResourcePolicyReconciler creates a new SriovResourcePolicyReconciler
func NewSriovResourcePolicyReconciler(client client.Client, nodeName, namespace string, deviceStateManager devicestate.DeviceState) *SriovResourcePolicyReconciler {
	return &SriovResourcePolicyReconciler{
		Client:             client,
		deviceStateManager: deviceStateManager,
		nodeName:           nodeName,
		namespace:          namespace,
		log:                klog.Background().WithName("SriovResourcePolicy"),
	}
}

// Reconcile handles reconciliation of SriovResourcePolicy and DeviceAttributes resources.
// It builds the full picture of which devices to advertise and which attributes to apply.
func (r *SriovResourcePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.Info("Starting reconcile", "request", req.NamespacedName, "watchedNamespace", r.namespace)

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

	// List all SriovResourcePolicy objects in the operator namespace
	resourcePolicyList := &sriovdrav1alpha1.SriovResourcePolicyList{}
	if err := r.List(ctx, resourcePolicyList, client.InNamespace(r.namespace)); err != nil {
		r.log.Error(err, "Failed to list SriovResourcePolicy objects", "namespace", r.namespace)
		return ctrl.Result{}, err
	}

	// List all DeviceAttributes objects in the operator namespace
	deviceAttrList := &sriovdrav1alpha1.DeviceAttributesList{}
	if err := r.List(ctx, deviceAttrList, client.InNamespace(r.namespace)); err != nil {
		r.log.Error(err, "Failed to list DeviceAttributes objects", "namespace", r.namespace)
		return ctrl.Result{}, err
	}

	// Find matching resource policies for this node
	var matchingPolicies []*sriovdrav1alpha1.SriovResourcePolicy
	for i := range resourcePolicyList.Items {
		policy := &resourcePolicyList.Items[i]
		if r.matchesNodeSelector(node.Labels, policy.Spec.NodeSelector) {
			matchingPolicies = append(matchingPolicies, policy)
		}
	}

	policyDevices := r.getPolicyDeviceMap(matchingPolicies, deviceAttrList.Items)
	if err := r.deviceStateManager.UpdatePolicyDevices(ctx, policyDevices); err != nil {
		r.log.Error(err, "Failed to update policy devices")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getPolicyDeviceMap builds the full map of device name -> attributes for all
// devices matched by the given policies. DeviceAttributes are resolved via
// each config's DeviceAttributesSelector.
func (r *SriovResourcePolicyReconciler) getPolicyDeviceMap(
	policies []*sriovdrav1alpha1.SriovResourcePolicy,
	allDeviceAttrs []sriovdrav1alpha1.DeviceAttributes,
) map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	policyDevices := make(map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute)

	if len(policies) == 0 {
		r.log.Info("No matching SriovResourcePolicy found for node", "nodeName", r.nodeName)
		return policyDevices
	}

	allocatableDevices := r.deviceStateManager.GetAllocatableDevices()

	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Name < policies[j].Name
	})
	for _, policy := range policies {
		r.log.V(2).Info("Processing policy",
			"policyName", policy.Name,
			"configCount", len(policy.Spec.Configs),
			"totalDevices", len(allocatableDevices))

		for _, config := range policy.Spec.Configs {
			resolvedAttrs := r.resolveDeviceAttributes(config.DeviceAttributesSelector, allDeviceAttrs)

			for deviceName, device := range allocatableDevices {
				if _, exists := policyDevices[deviceName]; exists {
					continue
				}

				if r.deviceMatchesFilters(device, config.ResourceFilters) {
					attrs := make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, len(resolvedAttrs))
					for k, v := range resolvedAttrs {
						attrs[k] = v
					}
					policyDevices[deviceName] = attrs
					r.log.V(2).Info("Device matches config filter",
						"deviceName", deviceName,
						"policyName", policy.Name,
						"device", device,
						"attributes", attrs)
				}
			}
		}
	}

	r.log.Info("Policy devices resolved",
		"matchingDevices", len(policyDevices),
		"totalDevices", len(allocatableDevices))
	r.log.V(2).Info("Policy devices details", "policyDevices", policyDevices)

	return policyDevices
}

// resolveDeviceAttributes finds all DeviceAttributes objects matching the
// given label selector and merges their attributes. When multiple objects
// match and define the same key, the value from the alphabetically last
// object name wins (deterministic).
func (r *SriovResourcePolicyReconciler) resolveDeviceAttributes(
	selector *metav1.LabelSelector,
	allDeviceAttrs []sriovdrav1alpha1.DeviceAttributes,
) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	if selector == nil {
		return nil
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		r.log.Error(err, "Invalid DeviceAttributesSelector")
		return nil
	}

	// Collect matching DeviceAttributes, sort by name for determinism
	var matched []sriovdrav1alpha1.DeviceAttributes
	for i := range allDeviceAttrs {
		da := allDeviceAttrs[i]
		if sel.Matches(labels.Set(da.Labels)) {
			matched = append(matched, da)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Name < matched[j].Name
	})

	merged := make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute)
	for _, da := range matched {
		for key, val := range da.Spec.Attributes {
			merged[key] = val
		}
	}

	return merged
}

// matchesNodeSelector checks if node labels match the given NodeSelector.
// A nil selector matches all nodes.
func (r *SriovResourcePolicyReconciler) matchesNodeSelector(nodeLabels map[string]string, nodeSelector *corev1.NodeSelector) bool {
	if nodeSelector == nil || len(nodeSelector.NodeSelectorTerms) == 0 {
		return true
	}

	// NodeSelectorTerms are ORed: at least one must match.
	for _, term := range nodeSelector.NodeSelectorTerms {
		ls := nodeSelectorTermToLabelSelector(term)
		selector, err := metav1.LabelSelectorAsSelector(ls)
		if err != nil {
			r.log.V(2).Info("Failed to parse NodeSelectorTerm", "error", err)
			continue
		}
		if selector.Matches(labels.Set(nodeLabels)) {
			return true
		}
	}
	return false
}

func nodeSelectorTermToLabelSelector(term corev1.NodeSelectorTerm) *metav1.LabelSelector {
	var exprs []metav1.LabelSelectorRequirement
	for _, req := range term.MatchExpressions {
		exprs = append(exprs, metav1.LabelSelectorRequirement{
			Key:      req.Key,
			Operator: metav1.LabelSelectorOperator(req.Operator),
			Values:   req.Values,
		})
	}
	return &metav1.LabelSelector{MatchExpressions: exprs}
}

// deviceMatchesFilters checks if a device matches any of the provided resource filters.
// Empty filters list matches all devices.
func (r *SriovResourcePolicyReconciler) deviceMatchesFilters(device resourceapi.Device, filters []sriovdrav1alpha1.ResourceFilter) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filter := range filters {
		if r.deviceMatchesFilter(device, filter) {
			return true
		}
	}

	return false
}

// deviceMatchesFilter checks if a device matches a specific resource filter
func (r *SriovResourcePolicyReconciler) deviceMatchesFilter(device resourceapi.Device, filter sriovdrav1alpha1.ResourceFilter) bool {
	if len(filter.Vendors) > 0 {
		vendorAttr, exists := device.Attributes[consts.AttributeVendorID]
		if !exists || vendorAttr.StringValue == nil {
			return false
		}
		if !stringSliceContains(filter.Vendors, *vendorAttr.StringValue) {
			return false
		}
	}

	if len(filter.Devices) > 0 {
		deviceAttr, exists := device.Attributes[consts.AttributeDeviceID]
		if !exists || deviceAttr.StringValue == nil {
			return false
		}
		if !stringSliceContains(filter.Devices, *deviceAttr.StringValue) {
			return false
		}
	}

	if len(filter.PciAddresses) > 0 {
		pciAttr, exists := device.Attributes[consts.AttributePciAddress]
		if !exists || pciAttr.StringValue == nil {
			return false
		}
		if !stringSliceContains(filter.PciAddresses, *pciAttr.StringValue) {
			return false
		}
	}

	if len(filter.PfNames) > 0 {
		pfAttr, exists := device.Attributes[consts.AttributePFName]
		if !exists || pfAttr.StringValue == nil {
			return false
		}
		if !stringSliceContains(filter.PfNames, *pfAttr.StringValue) {
			return false
		}
	}

	if len(filter.PfPciAddresses) > 0 {
		parentAttr, exists := device.Attributes[consts.AttributePfPciAddress]
		if !exists || parentAttr.StringValue == nil {
			return false
		}
		if !stringSliceContains(filter.PfPciAddresses, *parentAttr.StringValue) {
			return false
		}
	}

	if filter.LinkType != "" {
		linkAttr, exists := device.Attributes[consts.AttributeLinkType]
		if !exists || linkAttr.StringValue == nil {
			return false
		}
		if sriovdrav1alpha1.NormalizeLinkType(filter.LinkType) != *linkAttr.StringValue {
			return false
		}
	}

	// TODO: Implement driver checking if needed
	if len(filter.Drivers) > 0 {
		r.log.V(3).Info("Driver filtering not yet implemented", "deviceName", device.Name)
	}

	return true
}

func stringSliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovResourcePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	qHandler := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: r.namespace,
			Name:      resourcePolicySyncEventName,
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

	nodeEventHandler := handler.Funcs{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.Object.GetName() == r.nodeName {
				r.log.Info("Enqueuing sync for node create event", "node", e.Object.GetName())
				qHandler(w)
			}
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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
			if e.Object.GetName() == r.nodeName {
				r.log.Info("Enqueuing sync for node delete event", "node", e.Object.GetName())
				qHandler(w)
			}
		},
	}

	var eventChan = make(chan event.GenericEvent, 1)
	eventChan <- event.GenericEvent{Object: &sriovdrav1alpha1.SriovResourcePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: resourcePolicySyncEventName, Namespace: r.namespace}}}
	close(eventChan)

	namespacePredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == r.namespace
	})

	nodeMetadata := &metav1.PartialObjectMetadata{}
	nodeMetadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovdrav1alpha1.SriovResourcePolicy{}).
		Watches(nodeMetadata, nodeEventHandler).
		Watches(&sriovdrav1alpha1.SriovResourcePolicy{}, delayedEventHandler).
		Watches(&sriovdrav1alpha1.DeviceAttributes{}, delayedEventHandler).
		WithEventFilter(namespacePredicate).
		WatchesRawSource(source.Channel(eventChan, &handler.EnqueueRequestForObject{})).
		Complete(r)
}
