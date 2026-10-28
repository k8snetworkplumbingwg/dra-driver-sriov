package driver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

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
	attributeIfName       resourceapi.QualifiedName = consts.DriverName + "/ifName"
	attributeNADName      resourceapi.QualifiedName = consts.DriverName + "/netAttachDefName"
	attributeNADNamespace resourceapi.QualifiedName = consts.DriverName + "/netAttachDefNamespace"
)

// renderShadowConfig binds the single SR-IOV plugin in a config or conflist to an allocated VF.
func renderShadowConfig(source *nadv1.NetworkAttachmentDefinition, pci string) (string, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(source.Spec.Config), &config); err != nil {
		return "", fmt.Errorf("decode NAD %s/%s: %w", source.Namespace, source.Name, err)
	}
	if config == nil {
		return "", fmt.Errorf("NAD %s/%s contains an empty config", source.Namespace, source.Name)
	}
	sriov := config
	var plugins []map[string]json.RawMessage
	if raw, chained := config["plugins"]; chained {
		if err := json.Unmarshal(raw, &plugins); err != nil {
			return "", fmt.Errorf("decode plugins in NAD %s/%s: %w", source.Namespace, source.Name, err)
		}
		sriov = nil
		for _, plugin := range plugins {
			var pluginType string
			if err := json.Unmarshal(plugin["type"], &pluginType); err != nil || pluginType == "" {
				return "", fmt.Errorf("NAD %s/%s contains an invalid plugin type", source.Namespace, source.Name)
			}
			if pluginType != "sriov" {
				continue
			}
			if sriov != nil {
				return "", fmt.Errorf("NAD %s/%s contains multiple sriov plugins", source.Namespace, source.Name)
			}
			sriov = plugin
		}
	}
	var pluginType string
	if sriov == nil || json.Unmarshal(sriov["type"], &pluginType) != nil || pluginType != "sriov" {
		return "", fmt.Errorf("NAD %s/%s must contain exactly one sriov plugin", source.Namespace, source.Name)
	}
	var oldPCI, name string
	if raw, ok := sriov["deviceID"]; ok {
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
	sriov["deviceID"], _ = json.Marshal(pci)
	if plugins != nil {
		raw, err := json.Marshal(plugins)
		if err != nil {
			return "", fmt.Errorf("encode plugins in NAD %s/%s: %w", source.Namespace, source.Name, err)
		}
		config["plugins"] = raw
	}
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

// multusInterfaceName opts into PF-based naming only when the ifName attribute exists.
// The attribute is a prefix; the allocated VF index makes names unique per PF.
func multusInterfaceName(device *types.PreparedDevice) (string, error) {
	prefix, enabled := device.DeviceAttributes[string(attributeIfName)]
	if !enabled {
		return device.IfName, nil
	}
	if prefix.StringValue == nil || *prefix.StringValue == "" {
		return "", fmt.Errorf("device %s has an invalid ifName prefix: expected a nonempty string", device.Device.DeviceName)
	}
	vfID := device.DeviceAttributes[consts.AttributeVFID]
	if vfID.IntValue == nil || *vfID.IntValue < 0 {
		return "", fmt.Errorf("device %s requires a nonnegative integer vfID for ifName naming", device.Device.DeviceName)
	}
	name := fmt.Sprintf("%svf%d", *prefix.StringValue, *vfID.IntValue)
	// Linux interface names are at most IFNAMSIZ-1 bytes and cannot contain
	// whitespace, a slash, a colon, or a NUL byte.
	if len(name) > 15 || strings.ContainsAny(name, "/:\x00") || strings.IndexFunc(name, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("device %s generates invalid interface name %q (maximum 15 bytes; no whitespace, /, : or NUL)", device.Device.DeviceName, name)
	}
	if device.IfName != "" && device.IfName != name {
		return "", fmt.Errorf("device %s ifName %q conflicts with VfConfig interface %q", device.Device.DeviceName, name, device.IfName)
	}
	return name, nil
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
	// Validate all requested names before creating any temporary NADs.
	interfaceNames := make([]string, len(devices))
	usedNames := map[string]bool{}
	for i, device := range devices {
		name, err := multusInterfaceName(device)
		if err != nil {
			return err
		}
		if name != "" && usedNames[name] {
			return fmt.Errorf("duplicate requested Multus interface name %q", name)
		}
		usedNames[name] = true
		interfaceNames[i] = name
	}
	selections := []*nadv1.NetworkSelectionElement{}
	for i, device := range devices {
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
		selections = append(selections, &nadv1.NetworkSelectionElement{Name: key.Name, Namespace: key.Namespace, InterfaceRequest: interfaceNames[i]})
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := d.client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return fmt.Errorf("pod UID changed while preparing networks")
		}
		merged, err := nadutils.ParseNetworkAnnotation(current.Annotations[networksAnnotation], current.Namespace)
		if current.Annotations[networksAnnotation] == "" {
			merged, err = nil, nil
		}
		if err != nil {
			return err
		}
		for _, selection := range selections {
			for _, existing := range merged {
				sameAttachment := existing.Name == selection.Name && existing.Namespace == selection.Namespace
				if sameAttachment && existing.InterfaceRequest != selection.InterfaceRequest {
					return fmt.Errorf("Multus attachment %s already requests interface %q, wanted %q; recreate the Pod to change interface naming", selection.Name, existing.InterfaceRequest, selection.InterfaceRequest)
				}
				if !sameAttachment && selection.InterfaceRequest != "" && existing.InterfaceRequest == selection.InterfaceRequest {
					return fmt.Errorf("Multus interface name %q is already requested by attachment %s", selection.InterfaceRequest, existing.Name)
				}
			}
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
