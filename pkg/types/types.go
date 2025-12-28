package types

import (
	"encoding/json"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
)

// AllocatableDevices is a map of device pci address to dra device objects
type AllocatableDevices map[string]resourceapi.Device

// PreparedDevices is a slice of prepared devices
type PreparedDevices []*PreparedDevice

// PreparedDevicesByClaimID is a map of claim ID to prepared devices
type PreparedDevicesByClaimID map[k8stypes.UID]PreparedDevices

// PreparedClaimsByPodUID is a map of pod uid to map of claim ID to prepared devices
type PreparedClaimsByPodUID map[k8stypes.UID]PreparedDevicesByClaimID

type NetworkDataChanStruct struct {
	PreparedDevice    *PreparedDevice
	NetworkDeviceData *resourceapi.NetworkDeviceData
	CNIConfig         map[string]interface{}
	CNIResult         map[string]interface{}
}
type NetworkDataChanStructList []*NetworkDataChanStruct

// AddDeviceIDToNetConf adds the deviceID (PCI address) to the netconf
func AddDeviceIDToNetConf(originalConfig, deviceID string) (string, error) {
	// Unmarshal the existing configuration into a raw map
	var rawConfig map[string]interface{}
	if err := json.Unmarshal([]byte(originalConfig), &rawConfig); err != nil {
		return "", fmt.Errorf("failed to unmarshal existing config: %w", err)
	}

	// Set the deviceID (PCI address)
	rawConfig["deviceID"] = deviceID

	// Marshal the modified configuration back to a JSON string
	modifiedConfig, err := json.Marshal(rawConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified config: %w", err)
	}

	return string(modifiedConfig), nil
}

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

type PreparedDevice struct {
	Device              drapbv1.Device
	ClaimNamespacedName kubeletplugin.NamespacedObject
	ContainerEdits      *cdiapi.ContainerEdits
	Config              *configapi.VfConfig
	IfName              string
	PciAddress          string
	PodUID              string
	NetAttachDefConfig  string
	OriginalDriver      string // Store original driver for restoration during unprepare
}

type Checkpoint struct {
	Checksum checksum.Checksum `json:"checksum"`
	V1       *CheckpointV1     `json:"v1,omitempty"`
}

type CheckpointV1 struct {
	PreparedClaimsByPodUID PreparedClaimsByPodUID `json:"preparedClaimsByPodUID,omitempty"`
}

func NewCheckpoint() *Checkpoint {
	pc := &Checkpoint{
		Checksum: 0,
		V1: &CheckpointV1{
			PreparedClaimsByPodUID: make(PreparedClaimsByPodUID),
		},
	}
	return pc
}

func (cp *Checkpoint) MarshalCheckpoint() ([]byte, error) {
	cp.Checksum = 0
	out, err := json.Marshal(*cp)
	if err != nil {
		return nil, err
	}
	cp.Checksum = checksum.New(out)
	return json.Marshal(*cp)
}

func (cp *Checkpoint) UnmarshalCheckpoint(data []byte) error {
	return json.Unmarshal(data, cp)
}

func (cp *Checkpoint) VerifyChecksum() error {
	ck := cp.Checksum
	cp.Checksum = 0
	defer func() {
		cp.Checksum = ck
	}()
	out, err := json.Marshal(*cp)
	if err != nil {
		return err
	}
	return ck.Verify(out)
}
