package driver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

const (
	networksAnnotation                              = "k8s.v1.cni.cncf.io/networks"
	attachmentClaimLabel                            = consts.DriverName + "/attachment-claim"
	attributeNADName      resourceapi.QualifiedName = consts.DriverName + "/netAttachDefName"
	attributeNADNamespace resourceapi.QualifiedName = consts.DriverName + "/netAttachDefNamespace"
)

// shortcut: single-plugin netdevice NADs only; extend the renderer when the POC needs conflists.
func renderShadowConfig(source *nadv1.NetworkAttachmentDefinition, pci string) (string, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(source.Spec.Config), &config); err != nil {
		return "", fmt.Errorf("decode NAD %s/%s: %w", source.Namespace, source.Name, err)
	}
	if config == nil || string(config["type"]) != `"sriov"` || config["plugins"] != nil {
		return "", fmt.Errorf("NAD %s/%s must contain a single sriov plugin", source.Namespace, source.Name)
	}
	var oldPCI, name string
	if raw, ok := config["deviceID"]; ok {
		if err := json.Unmarshal(raw, &oldPCI); err != nil || (oldPCI != "" && oldPCI != pci) {
			return "", fmt.Errorf("NAD %s/%s has a conflicting deviceID", source.Namespace, source.Name)
		}
	}
	if raw, ok := config["name"]; ok {
		if err := json.Unmarshal(raw, &name); err != nil {
			return "", fmt.Errorf("invalid CNI name: %w", err)
		}
	}
	if name == "" {
		config["name"], _ = json.Marshal(source.Name)
	}
	config["deviceID"], _ = json.Marshal(pci)
	rendered, err := json.Marshal(config)
	return string(rendered), err
}

func networkTemplate(device *types.PreparedDevice, podNamespace string) (client.ObjectKey, error) {
	ref := client.ObjectKey{Name: device.Config.NetAttachDefName, Namespace: device.Config.NetAttachDefNamespace}
	if ref.Namespace == "" {
		ref.Namespace = podNamespace
	}
	name, hasName := device.DeviceAttributes[string(attributeNADName)]
	namespace, hasNamespace := device.DeviceAttributes[string(attributeNADNamespace)]
	if hasName || hasNamespace {
		if name.StringValue == nil || *name.StringValue == "" || (hasNamespace && namespace.StringValue == nil) {
			return ref, fmt.Errorf("device %s has an invalid NAD reference", device.Device.DeviceName)
		}
		mapped := client.ObjectKey{Name: *name.StringValue, Namespace: podNamespace}
		if hasNamespace && *namespace.StringValue != "" {
			mapped.Namespace = *namespace.StringValue
		}
		if ref.Name != "" && ref != mapped {
			return ref, fmt.Errorf("device NAD reference %s conflicts with VfConfig %s", mapped, ref)
		}
		ref = mapped
	}
	if ref.Name == "" {
		return ref, fmt.Errorf("device %s has no network template", device.Device.DeviceName)
	}
	return ref, nil
}

func (d *Driver) ensureMultusAttachments(ctx context.Context, claim *resourceapi.ResourceClaim, devices types.PreparedDevices) error {
	consumer := claim.Status.ReservedFor[0]
	if consumer.Resource != "pods" || consumer.APIGroup != "" {
		return fmt.Errorf("automatic Multus attachment requires a Pod consumer")
	}
	pod, err := d.client.CoreV1().Pods(claim.Namespace).Get(ctx, consumer.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pod.UID != consumer.UID || pod.Spec.NodeName != d.config.Flags.NodeName || pod.Spec.HostNetwork {
		return fmt.Errorf("claim consumer must be the reserved non-hostNetwork Pod on this node")
	}
	selections := []*nadv1.NetworkSelectionElement{}
	for _, device := range devices {
		if device.Config.Driver == "vfio-pci" {
			return fmt.Errorf("automatic Multus attachment requires a netdevice VF")
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s", pod.UID, claim.UID, device.Device.PoolName, device.Device.DeviceName)))
		key := client.ObjectKey{Namespace: pod.Namespace, Name: fmt.Sprintf("dra-sriov-%x", digest[:16])}
		shadow := &nadv1.NetworkAttachmentDefinition{}
		err := d.config.K8sClient.Get(ctx, key, shadow)
		if apierrors.IsNotFound(err) {
			ref, err := networkTemplate(device, pod.Namespace)
			if err != nil {
				return err
			}
			source := &nadv1.NetworkAttachmentDefinition{}
			if err := d.config.K8sClient.Get(ctx, ref, source); err != nil {
				return fmt.Errorf("read template %s: %w", ref, err)
			}
			rendered, err := renderShadowConfig(source, device.PciAddress)
			if err != nil {
				return err
			}
			shadow = &nadv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: key.Name, Namespace: key.Namespace,
					Labels:          map[string]string{attachmentClaimLabel: string(claim.UID)},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}},
				},
				Spec: nadv1.NetworkAttachmentDefinitionSpec{Config: rendered},
			}
			if err := d.config.K8sClient.Create(ctx, shadow); err != nil {
				return err
			}
			klog.FromContext(ctx).Info("Created Multus attachment", "pod", klog.KObj(pod), "nad", key, "pci", device.PciAddress, "source", ref)
		} else if err != nil {
			return err
		}
		if !slices.ContainsFunc(shadow.OwnerReferences, func(owner metav1.OwnerReference) bool { return owner.UID == pod.UID }) {
			return fmt.Errorf("NAD %s is not owned by Pod %s", key, pod.UID)
		}
		selections = append(selections, &nadv1.NetworkSelectionElement{Name: key.Name, Namespace: key.Namespace, InterfaceRequest: device.IfName})
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := d.client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return fmt.Errorf("Pod UID changed while preparing networks")
		}
		merged, err := nadutils.ParseNetworkAnnotation(current.Annotations[networksAnnotation], current.Namespace)
		if current.Annotations[networksAnnotation] == "" {
			merged, err = nil, nil
		}
		if err != nil {
			return err
		}
		for _, selection := range selections {
			if !slices.ContainsFunc(merged, func(existing *nadv1.NetworkSelectionElement) bool {
				return existing.Name == selection.Name && existing.Namespace == selection.Namespace
			}) {
				merged = append(merged, selection)
			}
		}
		value, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		patch, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{
			"resourceVersion": current.ResourceVersion,
			"annotations":     map[string]string{networksAnnotation: string(value)},
		}})
		if err != nil {
			return err
		}
		_, err = d.client.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
		return err
	})
}

func (d *Driver) deleteMultusAttachments(ctx context.Context, namespace string, claimUID k8stypes.UID) error {
	attachments := &nadv1.NetworkAttachmentDefinitionList{}
	if err := d.config.K8sClient.List(ctx, attachments, client.InNamespace(namespace), client.MatchingLabels{attachmentClaimLabel: string(claimUID)}); err != nil {
		return err
	}
	for i := range attachments.Items {
		if err := d.config.K8sClient.Delete(ctx, &attachments.Items[i]); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}
