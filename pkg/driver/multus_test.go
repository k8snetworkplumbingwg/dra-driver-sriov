package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

func TestRenderShadowConfig(t *testing.T) {
	source := &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "rail-a"},
		Spec:       nadv1.NetworkAttachmentDefinitionSpec{Config: `{"type":"sriov","cniVersion":"1.0.0","ipam":{"type":"host-local","subnet":"10.20.0.0/24"},"extension":9007199254740993}`},
	}
	rendered, err := renderShadowConfig(source, "0000:15:10.0")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rendered), &config); err != nil {
		t.Fatal(err)
	}
	if string(config["name"]) != `"rail-a"` || string(config["deviceID"]) != `"0000:15:10.0"` || string(config["extension"]) != "9007199254740993" || !strings.Contains(string(config["ipam"]), "10.20.0.0/24") {
		t.Fatalf("source identity, IPAM or precision changed: %s", rendered)
	}
	for _, invalid := range []string{`null`, `{}`, `{"type":"bridge"}`, `{"type":"sriov","deviceID":"0000:29:10.0"}`} {
		source.Spec.Config = invalid
		if _, err := renderShadowConfig(source, "0000:15:10.0"); err == nil {
			t.Errorf("accepted unsupported source %s", invalid)
		}
	}
}

func TestRenderShadowConfigConflist(t *testing.T) {
	const pci = "0000:15:10.0"
	for _, existingPCI := range []string{"", pci} {
		t.Run("existingPCI="+existingPCI, func(t *testing.T) {
			original := fmt.Sprintf(`{"cniVersion":"1.0.0","name":"rail-a","extension":9007199254740993,"plugins":[{"type":"sriov","vlan":0,"deviceID":%q,"ipam":{"type":"whereabouts","range":"172.16.100.0/24","gateway":"172.16.100.254"},"extension":9007199254740993},{"type":"sbr","extension":9007199254740993}]}`, existingPCI)
			source := &nadv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "template", Namespace: "system"},
				Spec:       nadv1.NetworkAttachmentDefinitionSpec{Config: original},
			}
			rendered, err := renderShadowConfig(source, pci)
			if err != nil {
				t.Fatal(err)
			}
			// Compare the entire document: only the nested deviceID may change.
			decoder := json.NewDecoder(strings.NewReader(rendered))
			decoder.UseNumber()
			var got map[string]interface{}
			if err := decoder.Decode(&got); err != nil {
				t.Fatal(err)
			}
			decoder = json.NewDecoder(strings.NewReader(original))
			decoder.UseNumber()
			var want map[string]interface{}
			if err := decoder.Decode(&want); err != nil {
				t.Fatal(err)
			}
			want["plugins"].([]interface{})[0].(map[string]interface{})["deviceID"] = pci
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("config changed beyond deviceID: got %s", rendered)
			}
			if source.Spec.Config != original {
				t.Fatal("source NAD was modified")
			}
		})
	}
}

func TestRenderShadowConfigConflistNameFallback(t *testing.T) {
	source := &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "rail-a"},
		Spec:       nadv1.NetworkAttachmentDefinitionSpec{Config: `{"plugins":[{"type":"sriov"},{"type":"sbr"}]}`},
	}
	rendered, err := renderShadowConfig(source, "0000:15:10.0")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rendered), &config); err != nil {
		t.Fatal(err)
	}
	if string(config["name"]) != `"rail-a"` || config["deviceID"] != nil {
		t.Fatalf("incorrect conflist root: %s", rendered)
	}
}

func TestRenderShadowConfigRejectsInvalidConflists(t *testing.T) {
	for name, config := range map[string]string{
		"null-list":       `{"plugins":null}`,
		"empty-list":      `{"plugins":[]}`,
		"not-a-list":      `{"plugins":{}}`,
		"scalar-entry":    `{"plugins":["sriov"]}`,
		"null-entry":      `{"plugins":[null]}`,
		"missing-type":    `{"plugins":[{}, {"type":"sriov"}]}`,
		"empty-type":      `{"plugins":[{"type":""}, {"type":"sriov"}]}`,
		"nonstring-type":  `{"plugins":[{"type":7}, {"type":"sriov"}]}`,
		"no-sriov":        `{"plugins":[{"type":"sbr"}]}`,
		"multiple-sriov":  `{"plugins":[{"type":"sriov"},{"type":"sriov"}]}`,
		"conflicting-pci": `{"plugins":[{"type":"sriov","deviceID":"0000:29:10.0"},{"type":"sbr"}]}`,
		"invalid-pci":     `{"plugins":[{"type":"sriov","deviceID":42},{"type":"sbr"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			source := &nadv1.NetworkAttachmentDefinition{Spec: nadv1.NetworkAttachmentDefinitionSpec{Config: config}}
			if _, err := renderShadowConfig(source, "0000:15:10.0"); err == nil {
				t.Fatal("accepted invalid conflist")
			}
		})
	}
}

func TestNetworkTemplate(t *testing.T) {
	device := &types.PreparedDevice{Config: &configapi.VfConfig{NetAttachDefName: "common", NetAttachDefNamespace: "infra"}}
	ref, err := networkTemplate(device, "workload")
	if err != nil || ref != (client.ObjectKey{Namespace: "infra", Name: "common"}) {
		t.Fatalf("VfConfig reference: %v, %v", ref, err)
	}
	device.Config = configapi.DefaultVfConfig()
	device.DeviceAttributes = map[string]resourceapi.DeviceAttribute{string(attributeNADName): {StringValue: ptr.To("rail-b")}}
	ref, err = networkTemplate(device, "workload")
	if err != nil || ref != (client.ObjectKey{Namespace: "workload", Name: "rail-b"}) {
		t.Fatalf("Pod-relative rail reference: %v, %v", ref, err)
	}
	device.Config.NetAttachDefName = "conflicting"
	if _, err := networkTemplate(device, "workload"); err == nil {
		t.Fatal("accepted conflicting reference")
	}
}

func TestEnsureMultusAttachments(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "test", UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: "node"}}
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "test", UID: "claim-uid"},
		Status:     resourceapi.ResourceClaimStatus{ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: pod.Name, UID: pod.UID}}},
	}
	source := &nadv1.NetworkAttachmentDefinition{ObjectMeta: metav1.ObjectMeta{Name: "template", Namespace: "test"}, Spec: nadv1.NetworkAttachmentDefinitionSpec{Config: `{"type":"sriov","name":"rail-a","ipam":{"type":"host-local","subnet":"10.20.0.0/24"}}`}}
	scheme := runtime.NewScheme()
	if err := nadv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()
	core := k8sfake.NewClientset(pod)
	d := &Driver{client: core, config: &types.Config{Flags: &types.Flags{NodeName: "node"}, K8sClient: flags.ClientSets{Interface: core, Client: objects}}}
	devices := types.PreparedDevices{{Device: drapbv1.Device{PoolName: "node", DeviceName: "vf0"}, Config: &configapi.VfConfig{NetAttachDefName: "template"}, PciAddress: "0000:15:10.0"}}
	for range 2 {
		if err := d.ensureMultusAttachments(ctx, claim, devices); err != nil {
			t.Fatal(err)
		}
	}
	shadows := &nadv1.NetworkAttachmentDefinitionList{}
	if err := objects.List(ctx, shadows, client.MatchingLabels{attachmentClaimLabel: string(claim.UID)}); err != nil || len(shadows.Items) != 1 {
		t.Fatalf("expected one temporary NAD: %v, %v", shadows.Items, err)
	}
	shadow := shadows.Items[0]
	if len(shadow.Annotations) != 0 || shadow.OwnerReferences[0].UID != pod.UID || !strings.Contains(shadow.Spec.Config, `"deviceID":"0000:15:10.0"`) {
		t.Fatalf("incorrect shadow: %+v", shadow)
	}
	updated, err := core.CoreV1().Pods("test").Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var selections []nadv1.NetworkSelectionElement
	if err := json.Unmarshal([]byte(updated.Annotations[networksAnnotation]), &selections); err != nil || len(selections) != 1 || selections[0].Name != shadow.Name {
		t.Fatalf("incorrect Pod selections: %v, %v", selections, err)
	}
	if err := objects.Get(ctx, client.ObjectKeyFromObject(source), source); err != nil || strings.Contains(source.Spec.Config, "deviceID") {
		t.Fatalf("template was modified: %s, %v", source.Spec.Config, err)
	}
	if err := d.deleteMultusAttachments(ctx, claim.Namespace, claim.UID); err != nil {
		t.Fatal(err)
	}
	if err := objects.List(ctx, shadows, client.MatchingLabels{attachmentClaimLabel: string(claim.UID)}); err != nil || len(shadows.Items) != 0 {
		t.Fatalf("temporary NAD not deleted: %v", err)
	}
}
