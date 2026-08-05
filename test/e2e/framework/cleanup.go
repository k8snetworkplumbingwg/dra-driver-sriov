package framework

import (
	"context"
	"fmt"

	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CleanupSpec describes post-test cleanup for a demo fixture.
// Each demo starts from scratch: cleanup deletes demo-owned policies/objects and
// does not re-create a baseline. The next demo's configuration applies what it needs.
type CleanupSpec struct {
	Namespaces            []string
	DeviceClasses         []string
	SriovResourcePolicies []string // names in DriverNamespace created/touched by the demo
	DeviceAttributes      []string // names in DriverNamespace
	DefaultPods           []string // pods in default namespace to delete
	DefaultNADs           []string // NetworkAttachmentDefinitions in default
}

// expectDeleted ignores NotFound; asserts any other delete error with a resource label.
func expectDeleted(err error, resource string) {
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred(), "deleting %s", resource)
}

// Cleanup deletes demo-owned objects. It does not restore any SriovResourcePolicy.
func (c *Clients) Cleanup(ctx context.Context, spec CleanupSpec) {
	for _, name := range spec.DefaultPods {
		err := c.Clientset.CoreV1().Pods("default").Delete(ctx, name, metav1.DeleteOptions{})
		expectDeleted(err, fmt.Sprintf("pod default/%s", name))
	}
	nadGVR := schema.GroupVersionResource{
		Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions",
	}
	for _, name := range spec.DefaultNADs {
		err := c.Dynamic.Resource(nadGVR).Namespace("default").Delete(ctx, name, metav1.DeleteOptions{})
		expectDeleted(err, fmt.Sprintf("NAD default/%s", name))
	}
	for _, name := range spec.DeviceClasses {
		err := c.Dynamic.Resource(DeviceClassGVR).Delete(ctx, name, metav1.DeleteOptions{})
		expectDeleted(err, fmt.Sprintf("DeviceClass %s", name))
	}
	for _, name := range spec.DeviceAttributes {
		err := c.Dynamic.Resource(DeviceAttributesGVR).Namespace(DriverNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		expectDeleted(err, fmt.Sprintf("DeviceAttribute %s/%s", DriverNamespace, name))
	}
	for _, name := range spec.SriovResourcePolicies {
		err := c.Dynamic.Resource(SriovResourcePolicyGVR).Namespace(DriverNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		expectDeleted(err, fmt.Sprintf("SriovResourcePolicy %s/%s", DriverNamespace, name))
	}
	for _, ns := range spec.Namespaces {
		// DeleteNamespace already maps NotFound to nil.
		err := c.DeleteNamespace(ctx, ns)
		Expect(err).NotTo(HaveOccurred(), "deleting namespace %s", ns)
	}
	for _, ns := range spec.Namespaces {
		c.WaitForNamespaceGone(ctx, ns)
	}
}
