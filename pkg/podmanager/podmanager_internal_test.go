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
		return checkpoint.V1.PreparedClaimsByPodUID
	}

	It("should leave a new claim out of the store when Set fails", func() {
		// The caller unprepares the devices on error; a stale entry would have
		// the next prepare return them as prepared.
		cm.fail = true

		Expect(pm.Set(podUID, claimUID, devices)).NotTo(Succeed())

		_, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeFalse())
		_, found = pm.GetDevicesByPodUID(podUID)
		Expect(found).To(BeFalse(), "a pod entry created for the failed Set must not linger")
	})

	It("should keep the previous devices of a claim when Set fails", func() {
		Expect(pm.Set(podUID, claimUID, devices)).To(Succeed())
		cm.fail = true

		replacement := draTypes.PreparedDevices{{Device: drapbv1.Device{DeviceName: "replacement"}}}
		Expect(pm.Set(podUID, claimUID, replacement)).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(devices))
		Expect(reload()[podUID][claimUID]).To(Equal(devices), "the checkpoint still holds what the store holds")
	})

	It("should keep the claim when DeleteClaim fails", func() {
		Expect(pm.Set(podUID, claimUID, devices)).To(Succeed())
		cm.fail = true

		Expect(pm.DeleteClaim(kubeletplugin.NamespacedObject{UID: claimUID})).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(devices))
	})

	It("should keep the pod when DeletePod fails", func() {
		Expect(pm.Set(podUID, claimUID, devices)).To(Succeed())
		cm.fail = true

		Expect(pm.DeletePod(podUID)).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(devices))
	})

	It("should keep the previous network data when the update fails", func() {
		Expect(pm.Set(podUID, claimUID, devices)).To(Succeed())
		previous := &resourceapi.NetworkDeviceData{InterfaceName: "net-old"}
		Expect(pm.UpdatePreparedDeviceNetworkData(devices[0], previous, 1)).To(Succeed())
		cm.fail = true

		Expect(pm.UpdatePreparedDeviceNetworkData(devices[0], &resourceapi.NetworkDeviceData{InterfaceName: "net-new"}, 2)).NotTo(Succeed())

		got, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(got[0].NetworkDeviceData).To(Equal(previous))
		Expect(got[0].NetworkDataSeq).To(Equal(uint64(1)), "the sequence has to describe the data the device kept")
		Expect(reload()[podUID][claimUID][0].NetworkDeviceData).To(Equal(previous))
	})
})
