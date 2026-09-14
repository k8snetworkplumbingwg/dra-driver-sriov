package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

const fieldManager = "dra-driver-sriov-e2e"

// AppliedObject records an object applied from a fixture for cleanup.
type AppliedObject struct {
	GVR        schema.GroupVersionResource
	Namespace  string
	Name       string
	Namespaced bool
}

// ApplyYAML applies all documents in a multi-doc YAML file as-is using server-side apply.
// Returns the list of applied objects (useful for selective cleanup).
func (c *Clients) ApplyYAML(ctx context.Context, path string) ([]AppliedObject, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return c.ApplyYAMLBytes(ctx, data)
}

// ApplyYAMLBytes applies multi-doc YAML content.
func (c *Clients) ApplyYAMLBytes(ctx context.Context, data []byte) ([]AppliedObject, error) {
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var applied []AppliedObject

	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return applied, fmt.Errorf("decode yaml: %w", err)
		}
		if len(obj.Object) == 0 || obj.GetKind() == "" {
			continue
		}

		gvk := obj.GroupVersionKind()
		mapping, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			c.Mapper.Reset()
			mapping, err = c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return applied, fmt.Errorf("rest mapping for %s: %w", gvk.String(), err)
			}
		}

		var resource dynamic.ResourceInterface
		namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
		if namespaced {
			ns := obj.GetNamespace()
			if ns == "" {
				ns = "default"
			}
			resource = c.Dynamic.Resource(mapping.Resource).Namespace(ns)
		} else {
			resource = c.Dynamic.Resource(mapping.Resource)
		}

		obj.SetManagedFields(nil)
		force := true
		_, err = resource.Apply(ctx, obj.GetName(), &obj, metav1.ApplyOptions{
			FieldManager: fieldManager,
			Force:        force,
		})
		if err != nil {
			return applied, fmt.Errorf("apply %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}

		applied = append(applied, AppliedObject{
			GVR:        mapping.Resource,
			Namespace:  obj.GetNamespace(),
			Name:       obj.GetName(),
			Namespaced: namespaced,
		})
	}
	return applied, nil
}

// DeleteNamespace deletes a namespace and waits until it is gone (best-effort timeout via context).
func (c *Clients) DeleteNamespace(ctx context.Context, name string) error {
	err := c.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// DeleteObject deletes a single applied object (best-effort).
func (c *Clients) DeleteObject(ctx context.Context, obj AppliedObject) error {
	var resource dynamic.ResourceInterface
	if obj.Namespaced {
		ns := obj.Namespace
		if ns == "" {
			ns = "default"
		}
		resource = c.Dynamic.Resource(obj.GVR).Namespace(ns)
	} else {
		resource = c.Dynamic.Resource(obj.GVR)
	}
	err := resource.Delete(ctx, obj.Name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ApplyAllDevicesPolicy applies the catch-all SriovResourcePolicy used to advertise all VFs.
func (c *Clients) ApplyAllDevicesPolicy(ctx context.Context) error {
	yamlBytes := []byte(`apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
kind: SriovResourcePolicy
metadata:
  name: all-devices
  namespace: dra-driver-sriov
spec:
  configs:
  - {}
`)
	_, err := c.ApplyYAMLBytes(ctx, yamlBytes)
	return err
}

// ResourceClaimGVR is the GVR for ResourceClaim.
var ResourceClaimGVR = schema.GroupVersionResource{
	Group:    "resource.k8s.io",
	Version:  "v1",
	Resource: "resourceclaims",
}

// DeviceClassGVR is the GVR for DeviceClass.
var DeviceClassGVR = schema.GroupVersionResource{
	Group:    "resource.k8s.io",
	Version:  "v1",
	Resource: "deviceclasses",
}

// SriovResourcePolicyGVR is the GVR for SriovResourcePolicy.
var SriovResourcePolicyGVR = schema.GroupVersionResource{
	Group:    "sriovnetwork.k8snetworkplumbingwg.io",
	Version:  "v1alpha1",
	Resource: "sriovresourcepolicies",
}

// DeviceAttributesGVR is the GVR for DeviceAttributes.
var DeviceAttributesGVR = schema.GroupVersionResource{
	Group:    "sriovnetwork.k8snetworkplumbingwg.io",
	Version:  "v1alpha1",
	Resource: "deviceattributes",
}
