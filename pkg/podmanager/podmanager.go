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
	mu                              sync.RWMutex
	preparedClaimsByPodUID          drasriovtypes.PreparedClaimsByPodUID
	pendingPreparedDevicesByClaimID drasriovtypes.PreparedDevicesByClaimID
	checkpointManager               checkpointmanager.CheckpointManager
}

func NewPodManager(config *drasriovtypes.Config) (*PodManager, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(config.DriverPluginPath())
	if err != nil {
		return nil, fmt.Errorf("unable to create checkpoint manager: %v", err)
	}

	checkpoints, err := checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	podmManager := &PodManager{
		mu:                              sync.RWMutex{},
		checkpointManager:               checkpointManager,
		preparedClaimsByPodUID:          make(drasriovtypes.PreparedClaimsByPodUID),
		pendingPreparedDevicesByClaimID: make(drasriovtypes.PreparedDevicesByClaimID),
	}

	for _, c := range checkpoints {
		if c == consts.DriverPluginCheckpointFile {
			klog.Infof("Found checkpoint: %s", c)
			checkpoint := drasriovtypes.NewCheckpoint()
			if err := checkpointManager.GetCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
				return nil, fmt.Errorf("unable to load checkpoint: %v", err)
			}
			if checkpoint.V1 == nil {
				return nil, fmt.Errorf("unable to load checkpoint: missing v1 data")
			}
			if checkpoint.V1.PreparedClaimsByPodUID == nil {
				checkpoint.V1.PreparedClaimsByPodUID = make(drasriovtypes.PreparedClaimsByPodUID)
			}
			if checkpoint.V1.PendingPreparedDevicesByClaimID == nil {
				checkpoint.V1.PendingPreparedDevicesByClaimID = make(drasriovtypes.PreparedDevicesByClaimID)
			}
			podmManager.preparedClaimsByPodUID = checkpoint.V1.PreparedClaimsByPodUID
			podmManager.pendingPreparedDevicesByClaimID = checkpoint.V1.PendingPreparedDevicesByClaimID
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
func (s *PodManager) Set(podUID types.UID, claimID types.UID, preparedDevices drasriovtypes.PreparedDevices) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldPodClaims, hadPod := s.preparedClaimsByPodUID[podUID]
	var oldClaimDevices drasriovtypes.PreparedDevices
	var hadClaim bool
	if hadPod {
		oldClaimDevices, hadClaim = oldPodClaims[claimID]
	}
	oldPending, hadPending := s.pendingPreparedDevicesByClaimID[claimID]

	if _, ok := s.preparedClaimsByPodUID[podUID]; !ok {
		s.preparedClaimsByPodUID[podUID] = make(drasriovtypes.PreparedDevicesByClaimID)
	}
	s.preparedClaimsByPodUID[podUID][claimID] = preparedDevices
	delete(s.pendingPreparedDevicesByClaimID, claimID)

	if err := s.syncToCheckpoint(); err != nil {
		if hadPod {
			s.preparedClaimsByPodUID[podUID] = oldPodClaims
			if hadClaim {
				oldPodClaims[claimID] = oldClaimDevices
			} else {
				delete(oldPodClaims, claimID)
			}
		} else {
			delete(s.preparedClaimsByPodUID, podUID)
		}
		if hadPending {
			s.pendingPreparedDevicesByClaimID[claimID] = oldPending
		} else {
			delete(s.pendingPreparedDevicesByClaimID, claimID)
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
	delete(s.preparedClaimsByPodUID, podUID)
	return s.syncToCheckpoint()
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
// tracked prepared device and syncs the checkpoint.
func (s *PodManager) UpdatePreparedDeviceNetworkData(preparedDevice *drasriovtypes.PreparedDevice, networkData *resourceapi.NetworkDeviceData) error {
	if preparedDevice == nil {
		return fmt.Errorf("prepared device is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	preparedDevice.SetNetworkDeviceData(networkData)
	return s.syncToCheckpoint()
}

// DeleteClaim removes all configurations associated with a given claim.
// NOTE: for now we only support one pod per claim as VFs are not shared between pods
func (s *PodManager) DeleteClaim(claim kubeletplugin.NamespacedObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldPreparedClaimsByPodUID := make(drasriovtypes.PreparedClaimsByPodUID, len(s.preparedClaimsByPodUID))
	for podUID, devicesByClaimID := range s.preparedClaimsByPodUID {
		oldPreparedClaimsByPodUID[podUID] = devicesByClaimID
	}
	oldPending, hadPending := s.pendingPreparedDevicesByClaimID[claim.UID]
	podsToDelete := []types.UID{}
	for uid, preparedDevicesByClaimID := range s.preparedClaimsByPodUID {
		_, found := preparedDevicesByClaimID[claim.UID]
		if found {
			podsToDelete = append(podsToDelete, uid)
			break
		}
	}

	if len(podsToDelete) > 0 {
		for _, uid := range podsToDelete {
			delete(s.preparedClaimsByPodUID, uid)
		}
		delete(s.pendingPreparedDevicesByClaimID, claim.UID)
		if err := s.syncToCheckpoint(); err != nil {
			s.preparedClaimsByPodUID = oldPreparedClaimsByPodUID
			if hadPending {
				s.pendingPreparedDevicesByClaimID[claim.UID] = oldPending
			}
			return err
		}
		return nil
	}
	if hadPending {
		delete(s.pendingPreparedDevicesByClaimID, claim.UID)
		if err := s.syncToCheckpoint(); err != nil {
			s.pendingPreparedDevicesByClaimID[claim.UID] = oldPending
			return err
		}
	}
	return nil
}

// BeginPrepare persists the cleanup metadata for a device before host mutation.
func (s *PodManager) BeginPrepare(claimID types.UID, preparedDevice *drasriovtypes.PreparedDevice) error {
	if preparedDevice == nil {
		return fmt.Errorf("prepared device is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldPending, hadPending := s.pendingPreparedDevicesByClaimID[claimID]
	for _, pendingDevice := range oldPending {
		if pendingDevice != nil && pendingDevice.PciAddress == preparedDevice.PciAddress {
			return fmt.Errorf("pending prepare already exists for claim %s and device %s", claimID, preparedDevice.PciAddress)
		}
	}
	s.pendingPreparedDevicesByClaimID[claimID] = append(
		s.pendingPreparedDevicesByClaimID[claimID],
		preparedDevice,
	)
	if err := s.syncToCheckpoint(); err != nil {
		if hadPending {
			s.pendingPreparedDevicesByClaimID[claimID] = oldPending
		} else {
			delete(s.pendingPreparedDevicesByClaimID, claimID)
		}
		return err
	}
	return nil
}

// AbortPendingPrepare removes durable cleanup metadata after a prepare rollback.
func (s *PodManager) AbortPendingPrepare(claimID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pending, found := s.pendingPreparedDevicesByClaimID[claimID]
	if !found {
		return nil
	}
	delete(s.pendingPreparedDevicesByClaimID, claimID)
	if err := s.syncToCheckpoint(); err != nil {
		s.pendingPreparedDevicesByClaimID[claimID] = pending
		return err
	}
	return nil
}

// AbortPendingPrepareDevice removes one device's durable cleanup metadata.
func (s *PodManager) AbortPendingPrepareDevice(claimID types.UID, pciAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pending, found := s.pendingPreparedDevicesByClaimID[claimID]
	if !found {
		return nil
	}
	remaining := make(drasriovtypes.PreparedDevices, 0, len(pending))
	removed := false
	for _, pendingDevice := range pending {
		if pendingDevice != nil && pendingDevice.PciAddress == pciAddress {
			removed = true
			continue
		}
		remaining = append(remaining, pendingDevice)
	}
	if !removed {
		return nil
	}
	if len(remaining) == 0 {
		delete(s.pendingPreparedDevicesByClaimID, claimID)
	} else {
		s.pendingPreparedDevicesByClaimID[claimID] = remaining
	}
	if err := s.syncToCheckpoint(); err != nil {
		s.pendingPreparedDevicesByClaimID[claimID] = pending
		return err
	}
	return nil
}

// PendingPrepares returns a snapshot of cleanup metadata awaiting commit or rollback.
func (s *PodManager) PendingPrepares() map[types.UID]drasriovtypes.PreparedDevices {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pending := make(map[types.UID]drasriovtypes.PreparedDevices, len(s.pendingPreparedDevicesByClaimID))
	for claimID, devices := range s.pendingPreparedDevicesByClaimID {
		pending[claimID] = append(drasriovtypes.PreparedDevices(nil), devices...)
	}
	return pending
}

func (s *PodManager) syncToCheckpoint() error {
	checkpoint := drasriovtypes.NewCheckpoint()
	checkpoint.V1.PreparedClaimsByPodUID = s.preparedClaimsByPodUID
	checkpoint.V1.PendingPreparedDevicesByClaimID = s.pendingPreparedDevicesByClaimID
	if err := s.checkpointManager.CreateCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
	}
	return nil
}
