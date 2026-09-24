package podmanager

import (
	"fmt"
	"sync"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// PodManager provides a thread-safe, centralized store for all prepared network devices
// across multiple Pods. It is indexed by the Pod's UID, and for each Pod, it maps
// claim IDs to their specific PreparedDevices.
type PodManager struct {
	mu                     sync.RWMutex
	preparedClaimsByPodUID drasriovtypes.PreparedClaimsByPodUID
	checkpointManager      checkpointmanager.CheckpointManager
}

func NewPodManager(config *drasriovtypes.Config) (*PodManager, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(config.DriverPluginPath())
	if err != nil {
		return nil, fmt.Errorf("unable to create checkpoint manager: %v", err)
	}
	return newPodManager(checkpointManager)
}

// NewPodManagerWithCheckpointManager constructs a PodManager using the given checkpoint
// manager. It is intended for tests that need to simulate checkpoint persistence failures.
func NewPodManagerWithCheckpointManager(checkpointManager checkpointmanager.CheckpointManager) (*PodManager, error) {
	return newPodManager(checkpointManager)
}

func newPodManager(checkpointManager checkpointmanager.CheckpointManager) (*PodManager, error) {
	checkpoints, err := checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	podmManager := &PodManager{
		mu:                     sync.RWMutex{},
		checkpointManager:      checkpointManager,
		preparedClaimsByPodUID: make(drasriovtypes.PreparedClaimsByPodUID),
	}

	for _, c := range checkpoints {
		if c == consts.DriverPluginCheckpointFile {
			klog.Infof("Found checkpoint: %s", c)
			checkpoint := drasriovtypes.NewCheckpoint()
			if err := checkpointManager.GetCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
				return nil, fmt.Errorf("unable to load checkpoint: %v", err)
			}
			podmManager.preparedClaimsByPodUID = checkpoint.V1.PreparedClaimsByPodUID
			klog.Infof("Loaded checkpoint with %d pods", len(podmManager.preparedClaimsByPodUID))
			return podmManager, nil
		}
	}

	checkpoint := drasriovtypes.NewCheckpoint()
	if err := checkpointManager.CreateCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}
	klog.Infof("Created checkpoint: %v", *checkpoint)

	return podmManager, nil
}

// Set stores the configuration for all prepared devices under a given Pod UID.
// If a configuration for the Pod UID or claim ID already exists, it will be overwritten.
// When the checkpoint cannot be written the store is left as it was, so a
// claim whose devices the caller then unprepares is not reported as prepared.
func (s *PodManager) Set(podUID types.UID, claimID types.UID, preparedDevices drasriovtypes.PreparedDevices) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	claims, hadPod := s.preparedClaimsByPodUID[podUID]
	if !hadPod {
		claims = make(drasriovtypes.PreparedDevicesByClaimID)
		s.preparedClaimsByPodUID[podUID] = claims
	}
	previous, hadClaim := claims[claimID]
	claims[claimID] = preparedDevices

	if err := s.syncToCheckpoint(); err != nil {
		switch {
		case hadClaim:
			claims[claimID] = previous
		case hadPod:
			delete(claims, claimID)
		default:
			delete(s.preparedClaimsByPodUID, podUID)
		}
		return err
	}
	return nil
}

// Get retrieves the configuration for a specific claim under a given Pod UID.
// It returns the Config and true if found, otherwise an empty Config and false.
func (s *PodManager) Get(podUID types.UID, claimID types.UID) (drasriovtypes.PreparedDevices, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if podConfigs, ok := s.preparedClaimsByPodUID[podUID]; ok {
		configs, found := podConfigs[claimID]
		return configs, found
	}
	return drasriovtypes.PreparedDevices{}, false
}

// GetDevicesByPodUID retrieves the configuration for all claims under a given Pod UID.
// It returns the Config and true if found, otherwise an empty Config and false.
func (s *PodManager) GetDevicesByPodUID(podUID types.UID) (drasriovtypes.PreparedDevices, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	claims, exists := s.preparedClaimsByPodUID[podUID]
	if !exists {
		return drasriovtypes.PreparedDevices{}, false
	}
	preparedDevices := drasriovtypes.PreparedDevices{}
	for _, devices := range claims {
		preparedDevices = append(preparedDevices, devices...)
	}
	return preparedDevices, true
}

// DeletePod removes all configurations associated with a given Pod UID.
func (s *PodManager) DeletePod(podUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	claims, found := s.preparedClaimsByPodUID[podUID]
	delete(s.preparedClaimsByPodUID, podUID)
	if err := s.syncToCheckpoint(); err != nil {
		if found {
			s.preparedClaimsByPodUID[podUID] = claims
		}
		return err
	}
	return nil
}

// GetByClaim retrieves the configuration for a specific claim.
func (s *PodManager) GetByClaim(claim kubeletplugin.NamespacedObject) (drasriovtypes.PreparedDevices, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	preparedDevices := drasriovtypes.PreparedDevices{}
	for _, preparedDevicesByClaimID := range s.preparedClaimsByPodUID {
		devices, found := preparedDevicesByClaimID[claim.UID]
		if found {
			preparedDevices = append(preparedDevices, devices...)
			return preparedDevices, true
		}
	}
	return preparedDevices, false
}

// UpdatePreparedDeviceNetworkData persists runtime network data on an already
// tracked prepared device, under the sequence of the observation it came from,
// and syncs the checkpoint. When the checkpoint cannot be written the device
// keeps its previous network data and sequence.
func (s *PodManager) UpdatePreparedDeviceNetworkData(preparedDevice *drasriovtypes.PreparedDevice, networkData *resourceapi.NetworkDeviceData, seq uint64) error {
	if preparedDevice == nil {
		return fmt.Errorf("prepared device is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, previousSeq := preparedDevice.NetworkDeviceData, preparedDevice.NetworkDataSeq
	preparedDevice.SetNetworkDeviceData(networkData)
	preparedDevice.NetworkDataSeq = seq
	if err := s.syncToCheckpoint(); err != nil {
		preparedDevice.NetworkDeviceData, preparedDevice.NetworkDataSeq = previous, previousSeq
		return err
	}
	return nil
}

// DeleteClaim removes the prepared devices of a claim. The pod's other claims
// are kept, and the pod itself is removed once it has no claim left.
// NOTE: for now we only support one pod per claim as VFs are not shared between pods
func (s *PodManager) DeleteClaim(claim kubeletplugin.NamespacedObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for podUID, claims := range s.preparedClaimsByPodUID {
		devices, found := claims[claim.UID]
		if !found {
			continue
		}
		delete(claims, claim.UID)
		if len(claims) == 0 {
			delete(s.preparedClaimsByPodUID, podUID)
		}
		if err := s.syncToCheckpoint(); err != nil {
			claims[claim.UID] = devices
			s.preparedClaimsByPodUID[podUID] = claims
			return err
		}
		return nil
	}
	return nil
}

func (s *PodManager) syncToCheckpoint() error {
	checkpoint := drasriovtypes.NewCheckpoint()
	checkpoint.V1.PreparedClaimsByPodUID = s.preparedClaimsByPodUID
	if err := s.checkpointManager.CreateCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
	}
	return nil
}
