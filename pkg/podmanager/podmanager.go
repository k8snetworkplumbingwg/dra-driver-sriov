package podmanager

import (
	"fmt"
	"maps"
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
	mu                      sync.RWMutex
	preparedClaimsByPodUID  drasriovtypes.PreparedClaimsByPodUID
	preparedClaimProvenance drasriovtypes.PreparedClaimProvenanceByPodUID
	checkpointManager       checkpointmanager.CheckpointManager
}

// GetClaimsByPodUID returns a shallow claim map copy; shared device records still require verification.
func (s *PodManager) GetClaimsByPodUID(podUID types.UID) drasriovtypes.PreparedDevicesByClaimID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.preparedClaimsByPodUID[podUID])
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

// newPodManager loads devices and provenance, leaving records without provenance cleanup-only.
func newPodManager(checkpointManager checkpointmanager.CheckpointManager) (*PodManager, error) {
	checkpoints, err := checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	podmManager := &PodManager{
		mu:                      sync.RWMutex{},
		checkpointManager:       checkpointManager,
		preparedClaimsByPodUID:  make(drasriovtypes.PreparedClaimsByPodUID),
		preparedClaimProvenance: make(drasriovtypes.PreparedClaimProvenanceByPodUID),
	}

	for _, c := range checkpoints {
		if c == consts.DriverPluginCheckpointFile {
			klog.Infof("Found checkpoint: %s", c)
			checkpoint := drasriovtypes.NewCheckpoint()
			if err := checkpointManager.GetCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
				return nil, fmt.Errorf("unable to load checkpoint: %v", err)
			}
			podmManager.preparedClaimsByPodUID = checkpoint.V1.PreparedClaimsByPodUID
			if checkpoint.V1.PreparedClaimProvenance != nil {
				podmManager.preparedClaimProvenance = checkpoint.V1.PreparedClaimProvenance
			}
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

// Set checkpoints devices and provenance, restoring both on failure; nil metadata leaves state unverified.
func (s *PodManager) Set(podUID types.UID, claimID types.UID, preparedDevices drasriovtypes.PreparedDevices, meta *drasriovtypes.PreparedClaimMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	claims, hadPod := s.preparedClaimsByPodUID[podUID]
	if !hadPod {
		claims = make(drasriovtypes.PreparedDevicesByClaimID)
		s.preparedClaimsByPodUID[podUID] = claims
	}
	previous, hadClaim := claims[claimID]
	claims[claimID] = preparedDevices

	// Copy the claim metadata map so persistence failure can restore it intact.
	previousProvenance := s.preparedClaimProvenance[podUID]
	provenance := maps.Clone(previousProvenance)
	if meta == nil {
		delete(provenance, claimID)
	} else {
		if provenance == nil {
			provenance = make(drasriovtypes.PreparedClaimProvenanceByClaim)
		}
		provenance[claimID] = *meta
	}
	if len(provenance) == 0 {
		delete(s.preparedClaimProvenance, podUID)
	} else {
		if s.preparedClaimProvenance == nil {
			s.preparedClaimProvenance = make(drasriovtypes.PreparedClaimProvenanceByPodUID)
		}
		s.preparedClaimProvenance[podUID] = provenance
	}

	if err := s.syncToCheckpoint(); err != nil {
		switch {
		case hadClaim:
			claims[claimID] = previous
		case hadPod:
			delete(claims, claimID)
		default:
			delete(s.preparedClaimsByPodUID, podUID)
		}
		if previousProvenance == nil {
			delete(s.preparedClaimProvenance, podUID)
		} else {
			s.preparedClaimProvenance[podUID] = previousProvenance
		}
		return err
	}
	return nil
}

// ClaimProvenance returns saved metadata or identifies existing records without it as cleanup-only.
func (s *PodManager) ClaimProvenance(podUID, claimID types.UID) (drasriovtypes.PreparedClaimMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	podProv, ok := s.preparedClaimProvenance[podUID]
	if !ok {
		if _, prepared := s.preparedClaimsByPodUID[podUID][claimID]; prepared {
			return drasriovtypes.PreparedClaimMeta{LegacyCleanupOnly: true}, true
		}
		return drasriovtypes.PreparedClaimMeta{}, false
	}
	meta, ok := podProv[claimID]
	if !ok {
		if _, prepared := s.preparedClaimsByPodUID[podUID][claimID]; prepared {
			return drasriovtypes.PreparedClaimMeta{LegacyCleanupOnly: true}, true
		}
		return drasriovtypes.PreparedClaimMeta{}, false
	}
	return meta, true
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

// DeletePod removes devices and provenance, restoring them if checkpoint persistence fails.
func (s *PodManager) DeletePod(podUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	claims, found := s.preparedClaimsByPodUID[podUID]
	provenance := s.preparedClaimProvenance[podUID]
	delete(s.preparedClaimsByPodUID, podUID)
	delete(s.preparedClaimProvenance, podUID)
	if err := s.syncToCheckpoint(); err != nil {
		if found {
			s.preparedClaimsByPodUID[podUID] = claims
		}
		if provenance != nil {
			s.preparedClaimProvenance[podUID] = provenance
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
// tracked prepared device and syncs the checkpoint. When the checkpoint cannot
// be written the device keeps its previous network data.
func (s *PodManager) UpdatePreparedDeviceNetworkData(preparedDevice *drasriovtypes.PreparedDevice, networkData *resourceapi.NetworkDeviceData) error {
	if preparedDevice == nil {
		return fmt.Errorf("prepared device is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	previous := preparedDevice.NetworkDeviceData
	preparedDevice.SetNetworkDeviceData(networkData)
	if err := s.syncToCheckpoint(); err != nil {
		preparedDevice.NetworkDeviceData = previous
		return err
	}
	return nil
}

// DeleteClaim removes a saved claim and its provenance, restoring both if checkpoint persistence fails.
func (s *PodManager) DeleteClaim(claim kubeletplugin.NamespacedObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for podUID, claims := range s.preparedClaimsByPodUID {
		devices, found := claims[claim.UID]
		if !found {
			continue
		}
		provenance := maps.Clone(s.preparedClaimProvenance[podUID])
		delete(claims, claim.UID)
		if podProv, ok := s.preparedClaimProvenance[podUID]; ok {
			delete(podProv, claim.UID)
			if len(podProv) == 0 {
				delete(s.preparedClaimProvenance, podUID)
			}
		}
		if len(claims) == 0 {
			delete(s.preparedClaimsByPodUID, podUID)
			delete(s.preparedClaimProvenance, podUID)
		}
		if err := s.syncToCheckpoint(); err != nil {
			claims[claim.UID] = devices
			s.preparedClaimsByPodUID[podUID] = claims
			if provenance != nil {
				s.preparedClaimProvenance[podUID] = provenance
			}
			return err
		}
		return nil
	}
	return nil
}

// syncToCheckpoint persists devices and provenance while the caller holds the write lock.
func (s *PodManager) syncToCheckpoint() error {
	checkpoint := drasriovtypes.NewCheckpoint()
	checkpoint.V1.PreparedClaimsByPodUID = s.preparedClaimsByPodUID
	checkpoint.V1.PreparedClaimProvenance = s.preparedClaimProvenance
	if err := s.checkpointManager.CreateCheckpoint(consts.DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
	}
	return nil
}
