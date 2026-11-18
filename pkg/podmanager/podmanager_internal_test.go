package podmanager

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	draTypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// failingCheckpointManager writes checkpoints for real until fail is set.
type failingCheckpointManager struct {
	checkpointmanager.CheckpointManager
	fail bool
}

func (f *failingCheckpointManager) CreateCheckpoint(key string, checkpoint checkpointmanager.Checkpoint) error {
	if f.fail {
		return errors.New("checkpoint write failed")
	}
	return f.CheckpointManager.CreateCheckpoint(key, checkpoint)
}

var _ = Describe("PodManager when the checkpoint cannot be written", func() {
	var (
		pm       *PodManager
		cm       *failingCheckpointManager
		podUID   = types.UID("pod-uid")
		claimUID = types.UID("claim-uid")
		devices  draTypes.PreparedDevices
		meta     = draTypes.PreparedClaimMeta{ConfigPolicyVersion: consts.DeviceClassAdminConfigPolicyVersion, ConfigFingerprint: "original-fingerprint"}
	)

	BeforeEach(func() {
		real, err := checkpointmanager.NewCheckpointManager(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		cm = &failingCheckpointManager{CheckpointManager: real}
		pm = &PodManager{checkpointManager: cm, preparedClaimsByPodUID: make(draTypes.PreparedClaimsByPodUID)}
		devices = draTypes.PreparedDevices{{
			Device:              drapbv1.Device{DeviceName: "test-device"},
			ClaimNamespacedName: kubeletplugin.NamespacedObject{UID: claimUID},
		}}
	})

	// reload reads the checkpoint back the way a restarted driver would.
	reload := func() draTypes.PreparedClaimsByPodUID {
		checkpoint := draTypes.NewCheckpoint()
		Expect(cm.GetCheckpoint(consts.DriverPluginCheckpointFile, checkpoint)).To(Succeed())
		Expect(checkpoint.V1.PreparedClaimProvenance[podUID][claimUID]).To(Equal(meta))
		return checkpoint.V1.PreparedClaimsByPodUID
	}
	// Check both in-memory provenance and its durable copy after failed mutations.
	expectProvenance := func() {
		saved, found := pm.ClaimProvenance(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(saved).To(Equal(meta))
		Expect(reload()[podUID]).To(HaveKey(claimUID))
	}

	It("should leave a new claim out of the store when Set fails", func() {
		// The caller unprepares the devices on error; a stale entry would have
		// the next prepare return them as prepared.
		cm.fail = true

		Expect(pm.Set(podUID, claimUID, devices, &meta)).NotTo(Succeed())

		_, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeFalse())
		_, found = pm.GetDevicesByPodUID(podUID)
		Expect(found).To(BeFalse(), "a pod entry created for the failed Set must not linger")
		_, found = pm.ClaimProvenance(podUID, claimUID)
		Expect(found).To(BeFalse(), "failed Set must not leave trusted provenance")
	})

	It("should keep the previous devices of a claim when Set fails", func() {
		Expect(pm.Set(podUID, claimUID, devices, &meta)).To(Succeed())
		cm.fail = true

		replacement := draTypes.PreparedDevices{{Device: drapbv1.Device{DeviceName: "replacement"}}}
		replacementMeta := meta
		replacementMeta.ConfigFingerprint = "replacement-fingerprint"
		Expect(pm.Set(podUID, claimUID, replacement, &replacementMeta)).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(devices))
		Expect(reload()[podUID][claimUID]).To(Equal(devices), "the checkpoint still holds what the store holds")
		expectProvenance()
	})

	It("should keep the claim when DeleteClaim fails", func() {
		Expect(pm.Set(podUID, claimUID, devices, &meta)).To(Succeed())
		cm.fail = true

		Expect(pm.DeleteClaim(kubeletplugin.NamespacedObject{UID: claimUID})).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(devices))
		expectProvenance()
	})

	It("should keep the pod when DeletePod fails", func() {
		Expect(pm.Set(podUID, claimUID, devices, &meta)).To(Succeed())
		cm.fail = true

		Expect(pm.DeletePod(podUID)).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(devices))
		expectProvenance()
	})

	It("should keep the previous network data when the update fails", func() {
		Expect(pm.Set(podUID, claimUID, devices, &meta)).To(Succeed())
		previous := &resourceapi.NetworkDeviceData{InterfaceName: "net-old"}
		Expect(pm.UpdatePreparedDeviceNetworkData(devices[0], previous)).To(Succeed())
		cm.fail = true

		Expect(pm.UpdatePreparedDeviceNetworkData(devices[0], &resourceapi.NetworkDeviceData{InterfaceName: "net-new"})).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got[0].NetworkDeviceData).To(Equal(previous))
		Expect(reload()[podUID][claimUID][0].NetworkDeviceData).To(Equal(previous))
	})

	It("reloads provenance, returns an independent membership map and keeps legacy state unverified", func() {
		Expect(pm.Set(podUID, claimUID, devices, &meta)).To(Succeed())
		restored, err := newPodManager(cm)
		Expect(err).NotTo(HaveOccurred())
		saved, found := restored.ClaimProvenance(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(saved).To(Equal(meta))
		membership := restored.GetClaimsByPodUID(podUID)
		delete(membership, claimUID)
		_, found = restored.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(restored.Set(podUID, claimUID, devices, nil)).To(Succeed())
		restored, err = newPodManager(cm)
		Expect(err).NotTo(HaveOccurred())
		saved, found = restored.ClaimProvenance(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(saved.LegacyCleanupOnly).To(BeTrue())
		Expect(saved.ConfigFingerprint).To(BeEmpty())
	})
})
