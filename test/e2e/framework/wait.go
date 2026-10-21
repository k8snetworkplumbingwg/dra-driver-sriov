package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultTimeout  = 5 * time.Minute
	DefaultInterval = 5 * time.Second
)

// WaitForPodReady waits until the named pod is Running and Ready.
func (c *Clients) WaitForPodReady(ctx context.Context, namespace, name string) *corev1.Pod {
	var pod *corev1.Pod
	Eventually(func(g Gomega) {
		var err error
		pod, err = c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "pod phase: %s", pod.Status.Phase)
		ready := false
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		g.Expect(ready).To(BeTrue(), "pod not Ready: %+v", pod.Status.Conditions)
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
	return pod
}

// WaitForDeploymentReady waits until the deployment has the desired number of ready replicas.
func (c *Clients) WaitForDeploymentReady(ctx context.Context, namespace, name string) *appsv1.Deployment {
	var dep *appsv1.Deployment
	Eventually(func(g Gomega) {
		var err error
		dep, err = c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		g.Expect(dep.Status.ReadyReplicas).To(Equal(desired),
			"ready=%d desired=%d", dep.Status.ReadyReplicas, desired)
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
	return dep
}

// WaitForNamespaceGone waits until the namespace is fully deleted.
func (c *Clients) WaitForNamespaceGone(ctx context.Context, name string) {
	Eventually(func(g Gomega) {
		_, err := c.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "namespace %s still exists: %v", name, err)
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
}

// WaitForResourceClaimAllocated waits until a ResourceClaim has allocation results.
func (c *Clients) WaitForResourceClaimAllocated(ctx context.Context, namespace, name string) *resourceapi.ResourceClaim {
	var claim *resourceapi.ResourceClaim
	Eventually(func(g Gomega) {
		var err error
		claim, err = c.Clientset.ResourceV1().ResourceClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(claim.Status.Allocation).NotTo(BeNil(), "claim %s/%s has no status.allocation", namespace, name)
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
	return claim
}

// WaitForResourceClaimDeviceNetworkData waits until the driver has recorded
// the network data of a device on the claim: status.devices has an entry for
// the driver whose networkData names ifName and carries at least one IP, and
// whose data carries the CNI result next to the VF config.
func (c *Clients) WaitForResourceClaimDeviceNetworkData(ctx context.Context, namespace, name, ifName string) {
	Eventually(func(g Gomega) {
		claim, err := c.Clientset.ResourceV1().ResourceClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(claim.Status.Devices).NotTo(BeEmpty(), "claim %s/%s has no status.devices", namespace, name)
		var matched bool
		for _, device := range claim.Status.Devices {
			if device.Driver != DriverName || device.NetworkData == nil || device.NetworkData.InterfaceName != ifName {
				// Other devices of this driver say nothing about this one.
				continue
			}
			g.Expect(device.NetworkData.IPs).NotTo(BeEmpty(), "claim %s/%s device %s has no IPs", namespace, name, device.Device)
			g.Expect(device.Data).NotTo(BeNil(), "claim %s/%s device %s has no data", namespace, name, device.Device)
			var data map[string]any
			g.Expect(json.Unmarshal(device.Data.Raw, &data)).To(Succeed())
			g.Expect(data).To(HaveKey("vfConfig"))
			g.Expect(data).To(HaveKey("cniResult"))
			matched = true
		}
		g.Expect(matched).To(BeTrue(), "claim %s/%s has no %s status.devices entry for %s", namespace, name, DriverName, ifName)
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
}

// WaitForResourceSlicesWithDevices waits until at least one ResourceSlice publishes devices.
func (c *Clients) WaitForResourceSlicesWithDevices(ctx context.Context) {
	Eventually(func(g Gomega) {
		list, err := c.Clientset.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(list.Items).NotTo(BeEmpty(), "no ResourceSlices found")
		total := 0
		for _, item := range list.Items {
			total += len(item.Spec.Devices)
		}
		g.Expect(total).To(BeNumerically(">", 0), "ResourceSlices have no devices")
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
}

// CountAllocatedDevicesInResourceSlices returns a rough count of devices across ResourceSlices.
func (c *Clients) CountAllocatedDevicesInResourceSlices(ctx context.Context) (int, error) {
	list, err := c.Clientset.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, item := range list.Items {
		total += len(item.Spec.Devices)
	}
	return total, nil
}

// WaitForDeviceAttributeString waits until at least one ResourceSlice device has
// attributes[attrKey].string == want (e.g. sriovnetwork.../resourceName == eth1_resource).
func (c *Clients) WaitForDeviceAttributeString(ctx context.Context, attrKey, want string) {
	Eventually(func(g Gomega) {
		list, err := c.Clientset.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(list.Items).NotTo(BeEmpty(), "no ResourceSlices found")

		found := false
		key := resourceapi.QualifiedName(attrKey)
		for _, item := range list.Items {
			for _, device := range item.Spec.Devices {
				s, ok := deviceAttributeString(device.Attributes, key)
				if ok && s == want {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		g.Expect(found).To(BeTrue(),
			"no ResourceSlice device has attribute %q=%q yet", attrKey, want)
	}).WithTimeout(DefaultTimeout).WithPolling(DefaultInterval).Should(Succeed())
}

// PodNamesForDeployment returns pod names matching the deployment selector.
func (c *Clients) PodNamesForDeployment(ctx context.Context, namespace, name string) ([]string, error) {
	dep, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pods.Items))
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no pods for deployment %s/%s", namespace, name)
	}
	return names, nil
}
