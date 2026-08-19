package types

import (
	"encoding/json"
	"fmt"
	"strings"

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

// RemoveOwnedVFAttributesFromNetConf removes native VF attributes from the
// sriov CNI configuration when those attributes are owned by DRA. This keeps
// the DRA and CNI paths from programming the same VF attribute in sequence.
func RemoveOwnedVFAttributesFromNetConf(originalConfig string, vfConfig *configapi.VFLinkConfig) (string, error) {
	if vfConfig == nil {
		return originalConfig, nil
	}

	keys := ownedVFNetConfKeys(vfConfig)
	if len(keys) == 0 {
		return originalConfig, nil
	}

	var rawConfig map[string]interface{}
	if err := json.Unmarshal([]byte(originalConfig), &rawConfig); err != nil {
		return "", fmt.Errorf("failed to unmarshal existing config: %w", err)
	}

	deleteOwnedVFNetConfKeys(rawConfig, keys)
	removeOwnedVFAttributesFromPlugins(rawConfig["plugins"], keys)
	removeOwnedVFAttributesFromPlugins(rawConfig["delegate"], keys)

	modifiedConfig, err := json.Marshal(rawConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified config: %w", err)
	}
	return string(modifiedConfig), nil
}

func ownedVFNetConfKeys(vfConfig *configapi.VFLinkConfig) map[string]struct{} {
	keys := make(map[string]struct{})
	if vfConfig.VLAN != nil || vfConfig.Qos != nil || vfConfig.VlanProto != nil {
		keys["vlan"] = struct{}{}
		keys["vlanQoS"] = struct{}{}
		keys["vlanProto"] = struct{}{}
	}
	if vfConfig.SpoofChk != nil {
		keys["spoofchk"] = struct{}{}
	}
	if vfConfig.Trust != nil {
		keys["trust"] = struct{}{}
	}
	if vfConfig.MinTxRate != nil || vfConfig.MaxTxRate != nil {
		keys["min_tx_rate"] = struct{}{}
		keys["max_tx_rate"] = struct{}{}
	}
	if vfConfig.LinkState != nil {
		keys["link_state"] = struct{}{}
	}
	return keys
}

func removeOwnedVFAttributesFromPlugins(value interface{}, keys map[string]struct{}) {
	switch typed := value.(type) {
	case []interface{}:
		for _, item := range typed {
			removeOwnedVFAttributesFromPlugins(item, keys)
		}
	case map[string]interface{}:
		if pluginType, ok := typed["type"].(string); ok && pluginType == "sriov" {
			deleteOwnedVFNetConfKeys(typed, keys)
		}
		removeOwnedVFAttributesFromPlugins(typed["plugins"], keys)
		removeOwnedVFAttributesFromPlugins(typed["delegate"], keys)
	}
}

func deleteOwnedVFNetConfKeys(config map[string]interface{}, keys map[string]struct{}) {
	for actualKey := range config {
		for ownedKey := range keys {
			if strings.EqualFold(actualKey, ownedKey) {
				delete(config, actualKey)
				break
			}
		}
	}
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
	MultusDeviceID      string
	MultusResourceName  string
	DeviceAttributes    map[string]resourceapi.DeviceAttribute
	NetworkDeviceData   *resourceapi.NetworkDeviceData
	PodUID              string
	NetAttachDefConfig  string
	OriginalDriver      string // Store original driver for restoration during unprepare
	// NativeVFAttributesOwned records that this prepared device owns native VF
	// attributes, independent of the configuration mode used after a restart.
	NativeVFAttributesOwned bool `json:"nativeVFAttributesOwned,omitempty"`
	// OriginalVFConfig holds the native VF attributes read before prepare changed
	// them. Unprepare writes these values back. Defaults such as spoofchk differ
	// per driver, so the driver restores the observed state and assumes nothing.
	OriginalVFConfig *configapi.VFLinkConfig `json:"originalVFConfig,omitempty"`
	// OriginalDriverKnown distinguishes an empty original driver from missing
	// cleanup metadata in a pending prepare transaction.
	OriginalDriverKnown bool `json:"originalDriverKnown,omitempty"`
}

func (p *PreparedDevice) ToKubeletPluginDevice(networkData *resourceapi.NetworkDeviceData) kubeletplugin.Device {
	if networkData == nil {
		networkData = p.NetworkDeviceData
	}
	return kubeletplugin.Device{
		Requests:     p.Device.GetRequestNames(),
		PoolName:     p.Device.GetPoolName(),
		DeviceName:   p.Device.GetDeviceName(),
		CDIDeviceIDs: p.Device.GetCdiDeviceIds(),
		Metadata: &kubeletplugin.DeviceMetadata{
			Attributes:  p.MetadataAttributes(),
			NetworkData: networkData,
		},
	}
}

func (p *PreparedDevice) SetNetworkDeviceData(networkData *resourceapi.NetworkDeviceData) {
	if networkData == nil {
		p.NetworkDeviceData = nil
		return
	}
	p.NetworkDeviceData = networkData.DeepCopy()
}

func (p *PreparedDevice) MetadataAttributes() map[string]resourceapi.DeviceAttribute {
	return p.DeviceAttributes
}

type Checkpoint struct {
	Checksum checksum.Checksum `json:"checksum"`
	V1       *CheckpointV1     `json:"v1,omitempty"`
}

type CheckpointV1 struct {
	PreparedClaimsByPodUID          PreparedClaimsByPodUID   `json:"preparedClaimsByPodUID,omitempty"`
	PendingPreparedDevicesByClaimID PreparedDevicesByClaimID `json:"pendingPreparedDevicesByClaimID,omitempty"`
}

func NewCheckpoint() *Checkpoint {
	pc := &Checkpoint{
		Checksum: 0,
		V1: &CheckpointV1{
			PreparedClaimsByPodUID:          make(PreparedClaimsByPodUID),
			PendingPreparedDevicesByClaimID: make(PreparedDevicesByClaimID),
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
