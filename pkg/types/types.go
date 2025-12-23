package types

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

const (
	// DRANetworkMACsAnnotation contains a JSON map of network name to MAC address
	// for DRA networks. This is set by KubeVirt on the virt-launcher pod to allow
	// the DRA driver to configure MAC addresses on SR-IOV VFs.
	DRANetworkMACsAnnotation = "kubevirt.io/dra-network-macs"
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

// AddMACToNetConf adds the MAC address to the netconf
func AddMACToNetConf(originalConfig, macAddress string) (string, error) {
	// Unmarshal the existing configuration into a raw map
	var rawConfig map[string]interface{}
	if err := json.Unmarshal([]byte(originalConfig), &rawConfig); err != nil {
		return "", fmt.Errorf("failed to unmarshal existing config: %w", err)
	}

	// Set the MAC address
	rawConfig["mac"] = macAddress

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

// CNIResultFile represents the path information for CNI result files
type CNIResultFile struct {
	HostPath      string // Path on the host where the file is stored
	ContainerPath string // Path inside the container where the file is mounted
}

// GetCNIResultFilePath returns the host and container paths for a CNI result file (one file per pod)
func GetCNIResultFilePath(baseDir, containerDir, podUID string) CNIResultFile {
	hostPath := filepath.Join(baseDir, fmt.Sprintf("%s.json", podUID))
	containerPath := filepath.Join(containerDir, "cni-results.json")
	return CNIResultFile{
		HostPath:      hostPath,
		ContainerPath: containerPath,
	}
}

// CreateCNIResultFile creates an empty placeholder CNI result file
func CreateCNIResultFile(filePath string) error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Check if file already exists (idempotent - multiple devices can try to create same pod file)
	if _, err := os.Stat(filePath); err == nil {
		return nil // File already exists, ok
	}

	// Create empty placeholder file (not JSON, just empty - lines added later)
	if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create CNI result file %s: %w", filePath, err)
	}

	return nil
}

// WriteCNIResultFile writes CNI results as line-by-line JSON to the file
// Format: each line is {"claim_name/request_name": [pci_address, ip, mac, ...]}
func WriteCNIResultFile(filePath, claimName, requestName, pciAddress string, networkData *resourceapi.NetworkDeviceData, cniResult map[string]interface{}) error {
	// Build the key as "claim_name/request_name"
	key := fmt.Sprintf("%s/%s", claimName, requestName)

	// Build list of PCI results with network data
	pciResults := []interface{}{
		pciAddress,
	}

	// // Add IPs if available
	// if networkData != nil && len(networkData.IPs) > 0 {
	// 	for _, ip := range networkData.IPs {
	// 		pciResults = append(pciResults, ip)
	// 	}
	// }

	// // Add MAC if available
	// if networkData != nil && networkData.HardwareAddress != "" {
	// 	pciResults = append(pciResults, networkData.HardwareAddress)
	// }

	// Create line: {"claim_name/request_name": [results]}
	line := map[string]interface{}{
		key: pciResults,
	}

	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("failed to marshal CNI result: %w", err)
	}

	// Append line to file
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write to %s: %w", filePath, err)
	}

	return nil
}
