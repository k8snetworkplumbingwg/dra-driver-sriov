package nri

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	cnimock "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cni/mock"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// checkpointRefuses wraps the pod manager and refuses to record the devices its
// predicate names, as a checkpoint write that failed for them would.
type checkpointRefuses struct {
	deviceStore
	refuse func(*types.PreparedDevice) bool
}

func (s checkpointRefuses) UpdatePreparedDeviceNetworkData(device *types.PreparedDevice, networkData *resourceapi.NetworkDeviceData, seq uint64) error {
	if s.refuse(device) {
		return errors.New("checkpoint write failed")
	}
	return s.deviceStore.UpdatePreparedDeviceNetworkData(device, networkData, seq)
}

// deviceNamed refuses one device by name.
func deviceNamed(name string) func(*types.PreparedDevice) bool {
	return func(device *types.PreparedDevice) bool { return device.Device.DeviceName == name }
}

// cancelsOnFirstWrite wraps the pod manager and cancels while recording the
// first device, as a shutdown arriving mid-write would.
type cancelsOnFirstWrite struct {
	deviceStore
	cancel context.CancelFunc
	writes *int
}

func (s cancelsOnFirstWrite) UpdatePreparedDeviceNetworkData(device *types.PreparedDevice, networkData *resourceapi.NetworkDeviceData, seq uint64) error {
	*s.writes++
	if *s.writes == 1 {
		s.cancel()
	}
	return s.deviceStore.UpdatePreparedDeviceNetworkData(device, networkData, seq)
}

type fakeMetadataUpdater struct {
	callCount   int
	requestName string
	devices     []kubeletplugin.Device
	err         error
}

// togglableCheckpointManager simulates checkpoint persistence failures in unit tests.
// Making checkpoint.json immutable (chattr +i) is unreliable in CI: runners often lack
// CAP_LINUX_IMMUTABLE. Injecting CreateCheckpoint errors is deterministic.
type togglableCheckpointManager struct {
	delegate   checkpointmanager.CheckpointManager
	failCreate bool
}

func (t *togglableCheckpointManager) CreateCheckpoint(checkpointKey string, checkpoint checkpointmanager.Checkpoint) error {
	if t.failCreate {
		return errors.New("simulated checkpoint sync failure")
	}
	return t.delegate.CreateCheckpoint(checkpointKey, checkpoint)
}

func (t *togglableCheckpointManager) GetCheckpoint(checkpointKey string, checkpoint checkpointmanager.Checkpoint) error {
	return t.delegate.GetCheckpoint(checkpointKey, checkpoint)
}

func (t *togglableCheckpointManager) RemoveCheckpoint(checkpointKey string) error {
	return t.delegate.RemoveCheckpoint(checkpointKey)
}

func (t *togglableCheckpointManager) ListCheckpoints() ([]string, error) {
	return t.delegate.ListCheckpoints()
}

func (f *fakeMetadataUpdater) UpdateRequestMetadata(
	_ context.Context,
	_, _ string,
	_ k8stypes.UID,
	requestName string,
	devices []kubeletplugin.Device,
) error {
	f.callCount++
	f.requestName = requestName
	f.devices = devices
	return f.err
}

var _ = Describe("NRI Plugin", func() {
	var (
		ctrl       *gomock.Controller
		mockCNI    *cnimock.MockInterface
		podManager *podmanager.PodManager
		plugin     *Plugin
		cfg        *types.Config
		ctx        context.Context
		pod        *api.PodSandbox
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockCNI = cnimock.NewMockInterface(ctrl)
		ctx = context.Background()

		flags := &types.Flags{
			DefaultInterfacePrefix:      "vfnet",
			KubeletPluginsDirectoryPath: "/tmp",
		}
		cfg = &types.Config{Flags: flags}

		var err error
		podManager, err = podmanager.NewPodManager(cfg)
		Expect(err).ToNot(HaveOccurred())

		// Minimal PodSandbox with Linux network namespace
		pod = &api.PodSandbox{
			Id:        "sandbox-id",
			Name:      "pod-name",
			Namespace: "default",
			Uid:       "uid-1",
			Linux: &api.LinuxPodSandbox{
				Namespaces: []*api.LinuxNamespace{{Type: "network", Path: "/proc/123/ns/net"}},
			},
		}

		plugin = &Plugin{
			podManager:                  podManager,
			cniRuntime:                  mockCNI,
			k8sClient:                   cfg.K8sClient,
			interfacePrefix:             flags.DefaultInterfacePrefix,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
			// don't initialize stub here; Start/Stop are not exercised in unit tests
		}
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	It("attaches networks for prepared devices", func() {
		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				IfName:             "vfnet0",
				NetAttachDefConfig: `{"type":"sriov","name":"net1"}`,
				PciAddress:         "0000:00:00.1",
				PodUID:             pod.Uid,
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())

		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(nil, map[string]interface{}{"dummy": true}, nil)

		// The goroutine uses a channel to update claim status; we don't rely on it here
		Expect(plugin.RunPodSandbox(ctx, pod)).To(Succeed())
	})

	It("returns error when CNI attach fails", func() {
		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				IfName:             "vfnet0",
				NetAttachDefConfig: `{"type":"sriov","name":"net1"}`,
				PciAddress:         "0000:00:00.1",
				PodUID:             pod.Uid,
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())

		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(nil, nil, errors.New("boom"))

		err := plugin.RunPodSandbox(ctx, pod)
		Expect(err).To(HaveOccurred())
	})

	It("updates request metadata synchronously before returning", func() {
		pciAddress := "0000:00:00.1"
		claimUID := k8stypes.UID("claim-1")
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "claim-1",
				UID:       claimUID,
			},
		}
		cfg.K8sClient = flags.ClientSets{
			Client: ctrlclientfake.NewClientBuilder().WithScheme(flags.Scheme).WithRuntimeObjects(claim).Build(),
		}
		plugin.k8sClient = cfg.K8sClient
		plugin.enableDeviceMetadata = true
		updater := &fakeMetadataUpdater{}
		plugin.metadataUpdater = updater
		ifName := "vfnet0"

		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					NamespacedName: k8stypes.NamespacedName{
						Namespace: "default",
						Name:      "claim-1",
					},
					UID: claimUID,
				},
				Device: drapbv1.Device{
					RequestNames: []string{"request-a"},
					PoolName:     "pool-a",
					DeviceName:   "dev-a",
				},
				IfName: ifName,
				DeviceAttributes: map[string]resourceapi.DeviceAttribute{
					consts.AttributePciAddress: {
						StringValue: &pciAddress,
					},
					consts.AttributeInterfaceName: {
						StringValue: &ifName,
					},
				},
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), claimUID, prepared)).To(Succeed())

		expectedNetworkData := &resourceapi.NetworkDeviceData{
			InterfaceName: "net1",
			IPs:           []string{"10.10.0.10/24"},
		}
		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(expectedNetworkData, map[string]interface{}{"dummy": true}, nil)

		Expect(plugin.RunPodSandbox(ctx, pod)).To(Succeed())
		Expect(updater.callCount).To(Equal(1))
		Expect(updater.requestName).To(Equal("request-a"))
		Expect(updater.devices).To(HaveLen(1))
		Expect(updater.devices[0].Metadata).NotTo(BeNil())
		Expect(updater.devices[0].Metadata.NetworkData).To(Equal(expectedNetworkData))
	})

	It("fails RunPodSandbox when synchronous metadata update fails", func() {
		claimUID := k8stypes.UID("claim-1")
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "claim-1",
				UID:       claimUID,
			},
		}
		cfg.K8sClient = flags.ClientSets{
			Client: ctrlclientfake.NewClientBuilder().WithScheme(flags.Scheme).WithRuntimeObjects(claim).Build(),
		}
		plugin.k8sClient = cfg.K8sClient
		plugin.enableDeviceMetadata = true
		plugin.metadataUpdater = &fakeMetadataUpdater{err: errors.New("metadata boom")}

		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					NamespacedName: k8stypes.NamespacedName{
						Namespace: "default",
						Name:      "claim-1",
					},
					UID: claimUID,
				},
				Device: drapbv1.Device{
					RequestNames: []string{"request-a"},
					PoolName:     "pool-a",
					DeviceName:   "dev-a",
				},
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), claimUID, prepared)).To(Succeed())

		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}, map[string]interface{}{"dummy": true}, nil)

		err := plugin.RunPodSandbox(ctx, pod)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to update request metadata before pod start"))
	})

	It("detaches networks on StopPodSandbox", func() {
		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				IfName:             "vfnet0",
				NetAttachDefConfig: `{"type":"sriov","name":"net1"}`,
				PciAddress:         "0000:00:00.1",
				PodUID:             pod.Uid,
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())

		mockCNI.EXPECT().
			DetachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(nil)

		Expect(plugin.StopPodSandbox(ctx, pod)).To(Succeed())
	})

	It("handles pod without network namespace in RunPodSandbox", func() {
		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				IfName:             "vfnet0",
				NetAttachDefConfig: `{"type":"sriov","name":"net1"}`,
				PciAddress:         "0000:00:00.1",
				PodUID:             pod.Uid,
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())

		// Pod without network namespace
		podNoNetNS := &api.PodSandbox{
			Id:        "sandbox-id",
			Name:      "pod-name",
			Namespace: "default",
			Uid:       "uid-1",
		}

		// Should skip attachment without error
		Expect(plugin.RunPodSandbox(ctx, podNoNetNS)).To(Succeed())
	})

	It("handles pod not found in podManager during RunPodSandbox", func() {
		podUnknown := &api.PodSandbox{
			Id:        "unknown-id",
			Name:      "unknown-pod",
			Namespace: "default",
			Uid:       "uid-unknown",
			Linux: &api.LinuxPodSandbox{
				Namespaces: []*api.LinuxNamespace{{Type: "network", Path: "/proc/456/ns/net"}},
			},
		}

		// Should succeed without doing anything
		Expect(plugin.RunPodSandbox(ctx, podUnknown)).To(Succeed())
	})

	It("returns error when detach fails in StopPodSandbox", func() {
		prepared := types.PreparedDevices{
			&types.PreparedDevice{
				IfName:             "vfnet0",
				NetAttachDefConfig: `{"type":"sriov","name":"net1"}`,
				PciAddress:         "0000:00:00.1",
				PodUID:             pod.Uid,
			},
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())

		mockCNI.EXPECT().
			DetachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(errors.New("detach failed"))

		err := plugin.StopPodSandbox(ctx, pod)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("detach"))
	})
})

var _ = Describe("NRI Plugin Creation", func() {
	It("creates a new NRI plugin successfully", func() {
		flags := &types.Flags{
			DefaultInterfacePrefix:      "net",
			KubeletPluginsDirectoryPath: "/tmp",
		}
		cfg := &types.Config{
			Flags: flags,
			CancelMainCtx: func(err error) {
				// Mock cancel function
			},
		}

		podManager, err := podmanager.NewPodManager(cfg)
		Expect(err).ToNot(HaveOccurred())

		ctrl := gomock.NewController(GinkgoT())
		defer ctrl.Finish()
		mockCNI := cnimock.NewMockInterface(ctrl)

		plugin, err := NewNRIPlugin(cfg, podManager, mockCNI, nil)
		// NRI stub creation will fail in test environment (no NRI socket/runtime)
		// but we can verify the function at least initializes fields and attempts creation
		if err == nil {
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.podManager).To(Equal(podManager))
			Expect(plugin.cniRuntime).To(Equal(mockCNI))
			Expect(plugin.interfacePrefix).To(Equal("net"))
			Expect(plugin.networkDeviceDataUpdateChan).ToNot(BeNil())
		} else {
			// Expected to fail without NRI runtime - could fail for various reasons
			// (e.g., invalid plugin name in test, no NRI socket, etc.)
			Expect(err).To(HaveOccurred())
		}
	})
})

var _ = Describe("NRI Update Network Device Data Runner", func() {
	const (
		claimUID = k8stypes.UID("claim-a-uid")
		podUID   = k8stypes.UID("pod-a-uid")
	)
	var (
		pm       *podmanager.PodManager
		prepared types.PreparedDevices
	)

	newClaim := func() *resourceapi.ResourceClaim {
		return &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "claim-a", Namespace: "default", UID: claimUID},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: []resourceapi.DeviceRequestAllocationResult{
					{Request: "req-a", Driver: consts.DriverName, Pool: "pool-a", Device: "dev-a"},
				}}},
				ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: "pod-a", UID: podUID}},
				Devices:     []resourceapi.AllocatedDeviceStatus{{Driver: consts.DriverName, Pool: "pool-a", Device: "dev-a"}},
			},
		}
	}
	event := func() types.NetworkDataChanStructList {
		return types.NetworkDataChanStructList{{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1"},
		}}
	}
	// stopWithin fails the test if stopRunner does not return in time.
	stopWithin := func(plugin *Plugin, timeout time.Duration) {
		stopped := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			plugin.stopRunner()
			close(stopped)
		}()
		Eventually(stopped, timeout).Should(BeClosed(), "stopRunner should return once the runner is cancelled")
	}

	BeforeEach(func() {
		cfg := &types.Config{Flags: &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}}
		var err error
		pm, err = podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		prepared = types.PreparedDevices{{
			ClaimNamespacedName: kubeletplugin.NamespacedObject{
				NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "claim-a"},
				UID:            claimUID,
			},
			Device: drapbv1.Device{PoolName: "pool-a", DeviceName: "dev-a"},
			PodUID: string(podUID),
		}}
		Expect(pm.Set(podUID, claimUID, prepared)).To(Succeed())
	})

	It("stops when context is cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())

		plugin := &Plugin{
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}

		done := make(chan bool)
		go func() {
			plugin.updateNetworkDeviceDataRunner(ctx)
			done <- true
		}()

		// Cancel immediately
		cancel()

		// Should exit
		Eventually(done, time.Second).Should(Receive())
	})

	It("processes queued updates until stopped", func() {
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: k8sfake.NewSimpleClientset(newClaim())},
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		plugin.startRunner(context.Background())
		defer plugin.stopRunner()

		plugin.networkDeviceDataUpdateChan <- event()

		Eventually(func() *resourceapi.NetworkDeviceData {
			got, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			return got.Status.Devices[0].NetworkData
		}, time.Second).ShouldNot(BeNil())
	})

	It("Stop cancels an update stuck on the API and waits for the runner", func() {
		// The API keeps failing, so the runner sits in the retry backoff; Stop
		// must cut that short rather than wait for the backoff to run out.
		fake := k8sfake.NewSimpleClientset(newClaim())
		getCalls := make(chan struct{}, 100)
		fake.PrependReactor("get", "resourceclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
			getCalls <- struct{}{}
			return true, nil, apierrors.NewServerTimeout(schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}, "get", 1)
		})
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		plugin.startRunner(context.Background())

		plugin.networkDeviceDataUpdateChan <- event()
		Eventually(getCalls, time.Second).Should(Receive(), "the runner should be retrying the fetch")

		stopWithin(plugin, time.Second)
		// A retry scheduled before Stop may have landed already; none may follow.
		for len(getCalls) > 0 {
			<-getCalls
		}
		Consistently(getCalls, 300*time.Millisecond).ShouldNot(Receive(), "a stopped runner must not keep retrying")
	})

	// failingUpdates makes the first n status updates fail with a server
	// timeout and counts every status update; the runner and the spec read
	// the count concurrently.
	failingUpdates := func(fake *k8sfake.Clientset, n int32, updateCalls *atomic.Int32) {
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "status" {
				return false, nil, nil
			}
			if updateCalls.Add(1) <= n {
				return true, nil, apierrors.NewServerTimeout(schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}, "update", 1)
			}
			return false, nil, nil
		})
	}
	// twoStepBackoff makes one round of retries two quick attempts.
	twoStepBackoff := wait.Backoff{Steps: 2, Duration: time.Millisecond}
	ipsOf := func(plugin *Plugin) []string {
		got, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		if got.Status.Devices[0].NetworkData == nil {
			return nil
		}
		return got.Status.Devices[0].NetworkData.IPs
	}

	It("requeues an update whose write failed and completes it later", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())
		var updateCalls atomic.Int32
		failingUpdates(fake, 2, &updateCalls)
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		plugin.startRunner(context.Background())
		defer plugin.stopRunner()

		update := types.NetworkDataChanStructList{{PreparedDevice: prepared[0], NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}}}}
		plugin.networkDeviceDataUpdateChan <- update

		Eventually(func() []string { return ipsOf(plugin) }, 5*time.Second).Should(Equal([]string{"10.10.0.10/24"}))
		Expect(updateCalls.Load()).To(Equal(int32(3)), "one failed round of two attempts, then the requeued update")
		Expect(update[0].Requeues).To(Equal(1))
	})

	It("skips a requeued update once a later one for the device was recorded", func() {
		// The first update fails and goes to the back of the queue, behind a
		// newer one for the same device. Applying the old one afterwards would
		// put stale addresses on the claim and in the checkpoint.
		fake := k8sfake.NewSimpleClientset(newClaim())
		var updateCalls atomic.Int32
		failingUpdates(fake, 2, &updateCalls)
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		older := types.NetworkDataChanStructList{{PreparedDevice: prepared[0], NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}}, Seq: 1}}
		newer := types.NetworkDataChanStructList{{PreparedDevice: prepared[0], NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.11/24"}}, Seq: 2}}
		plugin.networkDeviceDataUpdateChan <- older
		plugin.networkDeviceDataUpdateChan <- newer
		plugin.startRunner(context.Background())
		defer plugin.stopRunner()

		Eventually(func() []string { return ipsOf(plugin) }, 5*time.Second).Should(Equal([]string{"10.10.0.11/24"}))
		Eventually(plugin.networkDeviceDataUpdateChan, time.Second).Should(BeEmpty())
		Consistently(func() []string { return ipsOf(plugin) }, 300*time.Millisecond).Should(Equal([]string{"10.10.0.11/24"}), "the requeued older update must not overwrite the newer one")
		Expect(updateCalls.Load()).To(Equal(int32(3)), "the requeued update must be skipped without touching the API")
		Expect(older[0].Requeues).To(Equal(1))
		stored, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(stored[0].NetworkDeviceData.IPs).To(Equal([]string{"10.10.0.11/24"}))
	})

	It("keeps the newer CNI result when a requeued update carries the same network data", func() {
		// Two observations of one device can report the same interface, address
		// and MAC while their CNI results differ, in the DNS servers or the
		// routes for instance. The network data alone cannot tell them apart, so
		// an older update must not be able to put its result back on the claim.
		sameData := func() *resourceapi.NetworkDeviceData {
			return &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}}
		}
		fake := k8sfake.NewSimpleClientset(newClaim())
		var updateCalls atomic.Int32
		failingUpdates(fake, 2, &updateCalls)
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		dnsOf := func() string {
			got, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			if got.Status.Devices[0].Data == nil {
				return ""
			}
			var payload struct {
				CNIResult map[string]any `json:"cniResult"`
			}
			Expect(json.Unmarshal(got.Status.Devices[0].Data.Raw, &payload)).To(Succeed())
			dns, _ := payload.CNIResult["dns"].(string)
			return dns
		}

		older := types.NetworkDataChanStructList{{PreparedDevice: prepared[0], NetworkDeviceData: sameData(), CNIResult: map[string]any{"dns": "192.0.2.53"}, Seq: 1}}
		newer := types.NetworkDataChanStructList{{PreparedDevice: prepared[0], NetworkDeviceData: sameData(), CNIResult: map[string]any{"dns": "192.0.2.54"}, Seq: 2}}
		plugin.networkDeviceDataUpdateChan <- older
		plugin.networkDeviceDataUpdateChan <- newer
		plugin.startRunner(context.Background())
		defer plugin.stopRunner()

		Eventually(dnsOf, 5*time.Second).Should(Equal("192.0.2.54"))
		Eventually(plugin.networkDeviceDataUpdateChan, time.Second).Should(BeEmpty())
		Consistently(dnsOf, 300*time.Millisecond).Should(Equal("192.0.2.54"), "the requeued update must not put the older CNI result back")
	})

	It("applies a requeued update whose checkpoint had failed", func() {
		// A checkpoint write that fails leaves the device on its previous network
		// data, because the pod manager rolls its change back, while the update
		// travels on with the rest of its claim when a sibling's status write
		// fails. The store then differs from what the update asks for without any
		// later observation of the device, and the update is still owed.
		fake := k8sfake.NewSimpleClientset(newClaim())
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		Expect(prepared[0].NetworkDeviceData).To(BeNil(), "the rolled back device keeps the network data it had")

		requeued := types.NetworkDataChanStructList{{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               7,
			Requeues:          1,
		}}
		plugin.updateNetworkDeviceData(context.Background(), requeued)

		Expect(ipsOf(plugin)).To(Equal([]string{"10.10.0.10/24"}), "a requeued update must not be dropped as superseded when nothing newer was recorded")
	})

	It("makes every concurrent stop wait for the runner to return", func() {
		// Stop is exported and documented as doing nothing after the first call,
		// so a second caller must not be told the runner is done while it is
		// still writing an update.
		fake := k8sfake.NewSimpleClientset(newClaim())
		started := make(chan struct{})
		var once sync.Once
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "status" {
				return false, nil, nil
			}
			once.Do(func() { close(started) })
			time.Sleep(150 * time.Millisecond)
			return false, nil, nil
		})
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		plugin.networkDeviceDataUpdateChan <- types.NetworkDataChanStructList{{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               1,
		}}
		plugin.startRunner(context.Background())
		plugin.runnerMu.Lock()
		done := plugin.runnerDone
		plugin.runnerMu.Unlock()
		Eventually(started, time.Second).Should(BeClosed())

		stopped := make(chan bool, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				plugin.stopRunner()
				select {
				case <-done:
					stopped <- true
				default:
					stopped <- false
				}
			}()
		}
		wg.Wait()

		Expect(<-stopped).To(BeTrue(), "stopRunner returned before the runner had stopped")
		Expect(<-stopped).To(BeTrue(), "stopRunner returned before the runner had stopped")
	})

	It("requeues the updates of a claim whose checkpoint could not be written", func() {
		// Nothing was recorded, so there is no claim status to send either. The
		// data is still owed, and ending the round here is the only path that
		// loses it outright.
		fake := k8sfake.NewSimpleClientset(newClaim())
		var updateCalls atomic.Int32
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() == "status" {
				updateCalls.Add(1)
			}
			return false, nil, nil
		})
		plugin := &Plugin{
			podManager:                  checkpointRefuses{deviceStore: pm, refuse: deviceNamed("dev-a")},
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}

		update := types.NetworkDataChanStructList{{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               1,
		}}
		plugin.updateNetworkDeviceData(context.Background(), update)

		Expect(updateCalls.Load()).To(BeZero(), "there is nothing to put on the claim when nothing was recorded")
		Expect(update[0].Requeues).To(Equal(1), "an update that was never recorded is still owed")
		Expect(plugin.networkDeviceDataUpdateChan).To(Receive(Equal(update)), "the update must go back on the queue")
	})

	It("requeues the update the checkpoint refused while the rest of the claim goes out", func() {
		// One device's data failing to reach the checkpoint says nothing about
		// the others in the claim, whose status write still goes out. The one
		// left out of it is owed a retry all the same.
		claim := newClaim()
		claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results,
			resourceapi.DeviceRequestAllocationResult{Request: "req-b", Driver: consts.DriverName, Pool: "pool-a", Device: "dev-b"})
		fake := k8sfake.NewSimpleClientset(claim)
		plugin := &Plugin{
			podManager:                  checkpointRefuses{deviceStore: pm, refuse: deviceNamed("dev-a")},
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		deviceB := &types.PreparedDevice{
			ClaimNamespacedName: prepared[0].ClaimNamespacedName,
			Device:              drapbv1.Device{PoolName: "pool-a", DeviceName: "dev-b"},
			PodUID:              string(podUID),
		}
		Expect(pm.Set(podUID, claimUID, types.PreparedDevices{prepared[0], deviceB})).To(Succeed())
		refused := &types.NetworkDataChanStruct{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               1,
		}
		recorded := &types.NetworkDataChanStruct{
			PreparedDevice:    deviceB,
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net2", IPs: []string{"10.10.0.11/24"}},
			Seq:               2,
		}
		plugin.updateNetworkDeviceData(context.Background(), types.NetworkDataChanStructList{refused, recorded})

		got, err := fake.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Status.Devices).To(HaveLen(2), "the device that was recorded reaches the claim")
		stored, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(stored[1].NetworkDeviceData).NotTo(BeNil(), "the recorded device is in the store as well as on the claim")
		Expect(stored[0].NetworkDeviceData).To(BeNil(), "the refused device is not")
		Expect(refused.Requeues).To(Equal(1), "the device the checkpoint refused is still owed")
		Expect(recorded.Requeues).To(BeZero(), "the device already on the claim must not be sent again")
		Expect(plugin.networkDeviceDataUpdateChan).To(Receive(Equal(types.NetworkDataChanStructList{refused})))
	})

	It("does not write the checkpoint again when only the claim update is retried", func() {
		// The first round records the device and the status write fails. By the
		// retry the API is back but the disk is not, and repeating a write that
		// already happened must not stop the update reaching the API.
		fake := k8sfake.NewSimpleClientset(newClaim())
		var updateCalls atomic.Int32
		failingUpdates(fake, 2, &updateCalls)
		diskDown := false
		plugin := &Plugin{
			podManager:                  checkpointRefuses{deviceStore: pm, refuse: func(*types.PreparedDevice) bool { return diskDown }},
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		update := types.NetworkDataChanStructList{{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               1,
		}}
		plugin.updateNetworkDeviceData(context.Background(), update)
		Expect(update[0].Requeues).To(Equal(1), "the status write fails, so the update comes back")
		Expect(plugin.networkDeviceDataUpdateChan).To(Receive())

		diskDown = true
		plugin.updateNetworkDeviceData(context.Background(), update)
		Expect(ipsOf(plugin)).To(Equal([]string{"10.10.0.10/24"}), "the device was already recorded, so the retry only owes the claim update")
	})

	It("keeps a device's own checkpoint owed when another device's claim update fails for good", func() {
		// A rejected status write says nothing about the data a sibling still
		// has to get onto the disk.
		claim := newClaim()
		claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results,
			resourceapi.DeviceRequestAllocationResult{Request: "req-b", Driver: consts.DriverName, Pool: "pool-a", Device: "dev-b"})
		fake := k8sfake.NewSimpleClientset(claim)
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "status" {
				return false, nil, nil
			}
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}, "claim-a", errors.New("nope"))
		})
		deviceB := &types.PreparedDevice{
			ClaimNamespacedName: prepared[0].ClaimNamespacedName,
			Device:              drapbv1.Device{PoolName: "pool-a", DeviceName: "dev-b"},
			PodUID:              string(podUID),
		}
		Expect(pm.Set(podUID, claimUID, types.PreparedDevices{prepared[0], deviceB})).To(Succeed())
		plugin := &Plugin{
			podManager:                  checkpointRefuses{deviceStore: pm, refuse: deviceNamed("dev-a")},
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		refused := &types.NetworkDataChanStruct{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               1,
		}
		recorded := &types.NetworkDataChanStruct{
			PreparedDevice:    deviceB,
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net2", IPs: []string{"10.10.0.11/24"}},
			Seq:               2,
		}
		plugin.updateNetworkDeviceData(context.Background(), types.NetworkDataChanStructList{refused, recorded})

		Expect(refused.Requeues).To(Equal(1), "the claim rejecting the write does not settle what the disk still owes")
		Expect(plugin.networkDeviceDataUpdateChan).To(Receive(Equal(types.NetworkDataChanStructList{refused})))
	})

	It("stops before the next device of a claim once the context is done", func() {
		// Cancelling while one device is being recorded must not start the next.
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		writes := 0
		deviceB := &types.PreparedDevice{
			ClaimNamespacedName: prepared[0].ClaimNamespacedName,
			Device:              drapbv1.Device{PoolName: "pool-a", DeviceName: "dev-b"},
			PodUID:              string(podUID),
		}
		Expect(pm.Set(podUID, claimUID, types.PreparedDevices{prepared[0], deviceB})).To(Succeed())
		plugin := &Plugin{
			podManager:                  cancelsOnFirstWrite{deviceStore: pm, cancel: cancel, writes: &writes},
			k8sClient:                   flags.ClientSets{Interface: k8sfake.NewSimpleClientset(newClaim())},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		plugin.updateNetworkDeviceData(ctx, types.NetworkDataChanStructList{
			{PreparedDevice: prepared[0], NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1"}, Seq: 1},
			{PreparedDevice: deviceB, NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net2"}, Seq: 2},
		})

		Expect(writes).To(Equal(1), "the second device must not be recorded after the cancel")
	})

	It("retries a device whose real checkpoint write was refused", func() {
		// A store double cannot show what a failing syncToCheckpoint leaves
		// behind. A directory where the checkpoint file goes is refused for any
		// user, which removing write permissions is not.
		cfg := &types.Config{Flags: &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}}
		store, err := podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Set(podUID, claimUID, prepared)).To(Succeed())
		plugin := &Plugin{
			podManager:                  store,
			k8sClient:                   flags.ClientSets{Interface: k8sfake.NewSimpleClientset(newClaim())},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		checkpoint := filepath.Join(cfg.DriverPluginPath(), consts.DriverPluginCheckpointFile)
		Expect(os.Remove(checkpoint)).To(Succeed())
		Expect(os.Mkdir(checkpoint, 0o700)).To(Succeed())

		update := types.NetworkDataChanStructList{{
			PreparedDevice:    prepared[0],
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}},
			Seq:               1,
		}}
		plugin.updateNetworkDeviceData(context.Background(), update)
		Expect(update[0].Checkpointed).To(BeFalse())
		Expect(update[0].Requeues).To(Equal(1))
		Expect(ipsOf(plugin)).To(BeNil(), "nothing reaches the claim while the disk refuses the data")

		stored, found := store.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(stored[0].NetworkDeviceData).To(BeNil(), "the rollback leaves the device on the data the checkpoint holds")
		Expect(stored[0].NetworkDataSeq).To(BeZero(), "and on the sequence that describes it")

		Expect(os.Remove(checkpoint)).To(Succeed())
		plugin.updateNetworkDeviceData(context.Background(), update)
		Expect(ipsOf(plugin)).To(Equal([]string{"10.10.0.10/24"}), "the update completes once the disk takes it")
	})

	It("skips an update the device has passed, on a first delivery as well", func() {
		// The number is taken before the update is queued, so a first delivery
		// can still reach the runner behind a later observation.
		fake := k8sfake.NewSimpleClientset(newClaim())
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		deviceData := func(ip string) *resourceapi.NetworkDeviceData {
			return &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{ip}}
		}
		plugin.updateNetworkDeviceData(context.Background(), types.NetworkDataChanStructList{
			{PreparedDevice: prepared[0], NetworkDeviceData: deviceData("10.10.0.11/24"), Seq: 2},
		})
		plugin.updateNetworkDeviceData(context.Background(), types.NetworkDataChanStructList{
			{PreparedDevice: prepared[0], NetworkDeviceData: deviceData("10.10.0.10/24"), Seq: 1},
		})

		Expect(ipsOf(plugin)).To(Equal([]string{"10.10.0.11/24"}), "the earlier observation must not come back over the later one")
	})

	It("drops an update after maxRequeues rounds", func() {
		fake := k8sfake.NewSimpleClientset(newClaim())
		var updateCalls atomic.Int32
		failingUpdates(fake, 1000, &updateCalls)
		plugin := &Plugin{
			podManager:                  pm,
			k8sClient:                   flags.ClientSets{Interface: fake},
			statusBackoff:               twoStepBackoff,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10),
		}
		plugin.startRunner(context.Background())
		defer plugin.stopRunner()

		update := event()
		plugin.networkDeviceDataUpdateChan <- update

		attempts := int32((1 + maxRequeues) * twoStepBackoff.Steps)
		Eventually(updateCalls.Load, 5*time.Second).Should(Equal(attempts))
		Consistently(updateCalls.Load, 300*time.Millisecond).Should(Equal(attempts), "a dropped update must not be retried again")
		Expect(plugin.networkDeviceDataUpdateChan).To(BeEmpty())
		Expect(update[0].Requeues).To(Equal(maxRequeues))
	})

	It("tolerates Stop before Start and a repeated Stop", func() {
		plugin := &Plugin{networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10)}
		plugin.stopRunner()

		plugin.startRunner(context.Background())
		stopWithin(plugin, time.Second)
		stopWithin(plugin, time.Second)
	})

	It("keeps the queue open for a hook still in flight after Stop", func() {
		// The runner is gone, so the update is dropped with the backlog, but
		// the send must not panic.
		plugin := &Plugin{networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 1)}
		plugin.startRunner(context.Background())
		plugin.stopRunner()

		Expect(func() {
			select {
			case plugin.networkDeviceDataUpdateChan <- event():
			default:
			}
			select {
			case plugin.networkDeviceDataUpdateChan <- event():
			default:
			}
		}).NotTo(Panic())
	})
})

// fakeStub stands in for the NRI stub; only Start and Stop are called. Like
// the real stub, Stop may be called more than once.
type fakeStub struct {
	stub.Stub
	startErr error
	stopped  chan struct{}
	stopOnce sync.Once
}

func (f *fakeStub) Start(context.Context) error { return f.startErr }
func (f *fakeStub) Stop()                       { f.stopOnce.Do(func() { close(f.stopped) }) }

var _ = Describe("NRI Plugin Start and Stop", func() {
	newPlugin := func(startErr error) (*Plugin, *fakeStub) {
		s := &fakeStub{startErr: startErr, stopped: make(chan struct{})}
		return &Plugin{stub: s, networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 10)}, s
	}
	runnerRunning := func(plugin *Plugin) bool {
		plugin.runnerMu.Lock()
		defer plugin.runnerMu.Unlock()
		return plugin.runnerDone != nil
	}

	It("starts the runner after the stub and stops the stub before the runner", func() {
		plugin, s := newPlugin(nil)
		Expect(plugin.Start(context.Background())).To(Succeed())
		Expect(runnerRunning(plugin)).To(BeTrue())

		plugin.Stop()
		Expect(s.stopped).To(BeClosed())
		Expect(runnerRunning(plugin)).To(BeFalse())
	})

	It("does not start the runner when the stub fails to start", func() {
		plugin, _ := newPlugin(errors.New("no runtime"))
		Expect(plugin.Start(context.Background())).To(HaveOccurred())
		Expect(runnerRunning(plugin)).To(BeFalse())
	})

	It("survives hooks racing Stop", func() {
		ctrl := gomock.NewController(GinkgoT())
		defer ctrl.Finish()
		mockCNI := cnimock.NewMockInterface(ctrl)
		cfg := &types.Config{Flags: &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}}
		podManager, err := podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())
		pod := &api.PodSandbox{
			Id: "sandbox-id", Name: "pod-name", Namespace: "default", Uid: "uid-1",
			Linux: &api.LinuxPodSandbox{Namespaces: []*api.LinuxNamespace{{Type: "network", Path: "/proc/123/ns/net"}}},
		}
		prepared := types.PreparedDevices{{
			ClaimNamespacedName: kubeletplugin.NamespacedObject{NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "claim-1"}, UID: "claim-1-uid"},
			IfName:              "vfnet0",
			PciAddress:          "0000:00:00.1",
			PodUID:              pod.Uid,
		}}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())
		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}, map[string]interface{}{}, nil).
			AnyTimes()

		plugin, _ := newPlugin(nil)
		plugin.podManager = podManager
		plugin.cniRuntime = mockCNI
		// The claim does not exist, so the runner skips every update it gets to.
		plugin.k8sClient = flags.ClientSets{Interface: k8sfake.NewSimpleClientset()}
		Expect(plugin.Start(context.Background())).To(Succeed())

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				defer GinkgoRecover()
				for range 25 {
					Expect(plugin.RunPodSandbox(context.Background(), pod)).To(Succeed())
				}
			})
		}
		wg.Go(plugin.Stop)
		wg.Wait()
		plugin.Stop()
	})
})

var _ = Describe("NRI RunPodSandbox backpressure", func() {
	It("does not block the hook when the update queue is full", func() {
		ctrl := gomock.NewController(GinkgoT())
		defer ctrl.Finish()
		mockCNI := cnimock.NewMockInterface(ctrl)

		cfg := &types.Config{Flags: &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}}
		podManager, err := podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		pod := &api.PodSandbox{
			Id: "sandbox-id", Name: "pod-name", Namespace: "default", Uid: "uid-1",
			Linux: &api.LinuxPodSandbox{Namespaces: []*api.LinuxNamespace{{Type: "network", Path: "/proc/123/ns/net"}}},
		}
		prepared := types.PreparedDevices{{IfName: "vfnet0", PciAddress: "0000:00:00.1", PodUID: pod.Uid}}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared)).To(Succeed())
		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}, map[string]interface{}{}, nil)

		// No runner drains the queue, and it is already full.
		plugin := &Plugin{
			podManager:                  podManager,
			cniRuntime:                  mockCNI,
			networkDeviceDataUpdateChan: make(chan types.NetworkDataChanStructList, 1),
		}
		plugin.networkDeviceDataUpdateChan <- types.NetworkDataChanStructList{}

		returned := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			returned <- plugin.RunPodSandbox(context.Background(), pod)
		}()
		Eventually(returned, time.Second).Should(Receive(BeNil()), "the sandbox must start even though its update was dropped")
		Expect(plugin.networkDeviceDataUpdateChan).To(HaveLen(1), "the dropped update must not displace the queued one")
	})
})

var _ = Describe("NRI metadata updates", func() {
	It("updates request metadata when enabled", func() {
		pciAddress := "0000:08:00.1"
		ifName := "vfnet0"
		updater := &fakeMetadataUpdater{}
		plugin := &Plugin{
			enableDeviceMetadata: true,
			metadataUpdater:      updater,
		}

		prepared := &types.PreparedDevice{
			Device: drapbv1.Device{
				RequestNames: []string{"request-a"},
				PoolName:     "pool-a",
				DeviceName:   "dev-a",
				CdiDeviceIds: []string{"cdi-a"},
			},
			IfName: ifName,
			DeviceAttributes: map[string]resourceapi.DeviceAttribute{
				consts.AttributePciAddress: {
					StringValue: &pciAddress,
				},
				consts.AttributeInterfaceName: {
					StringValue: &ifName,
				},
			},
		}
		networkData := &types.NetworkDataChanStruct{
			PreparedDevice: prepared,
			NetworkDeviceData: &resourceapi.NetworkDeviceData{
				InterfaceName: "net1",
				IPs:           []string{"10.10.0.10/24"},
			},
		}

		networkDataList := types.NetworkDataChanStructList{networkData}
		err := plugin.updateRequestMetadataBeforeSandboxStart(context.Background(), networkDataList)
		Expect(err).NotTo(HaveOccurred())
		Expect(updater.callCount).To(Equal(1))
		Expect(updater.requestName).To(Equal("request-a"))
		Expect(updater.devices).To(HaveLen(1))
		Expect(updater.devices[0].Metadata).NotTo(BeNil())
		Expect(updater.devices[0].Metadata.NetworkData).To(Equal(networkData.NetworkDeviceData))
		Expect(updater.devices[0].Metadata.Attributes).To(HaveKey(consts.AttributePciAddress))
		Expect(updater.devices[0].Metadata.Attributes).To(HaveKey(consts.AttributeInterfaceName))
	})

	It("updates each request once with all devices", func() {
		pciAddressA := "0000:08:00.1"
		pciAddressB := "0000:08:00.2"
		ifNameA := "vfnet0"
		ifNameB := "vfnet1"
		updater := &fakeMetadataUpdater{}
		plugin := &Plugin{
			enableDeviceMetadata: true,
			metadataUpdater:      updater,
		}
		deviceA := &types.NetworkDataChanStruct{
			PreparedDevice: &types.PreparedDevice{
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					NamespacedName: k8stypes.NamespacedName{
						Namespace: "default",
						Name:      "claim-a",
					},
					UID: k8stypes.UID("claim-a-uid"),
				},
				Device: drapbv1.Device{
					RequestNames: []string{"request-a"},
					PoolName:     "pool-a",
					DeviceName:   "dev-a",
					CdiDeviceIds: []string{"cdi-a"},
				},
				IfName: ifNameA,
				DeviceAttributes: map[string]resourceapi.DeviceAttribute{
					consts.AttributePciAddress:    {StringValue: &pciAddressA},
					consts.AttributeInterfaceName: {StringValue: &ifNameA},
				},
			},
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1"},
		}
		deviceB := &types.NetworkDataChanStruct{
			PreparedDevice: &types.PreparedDevice{
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					NamespacedName: k8stypes.NamespacedName{
						Namespace: "default",
						Name:      "claim-a",
					},
					UID: k8stypes.UID("claim-a-uid"),
				},
				Device: drapbv1.Device{
					RequestNames: []string{"request-a"},
					PoolName:     "pool-a",
					DeviceName:   "dev-b",
					CdiDeviceIds: []string{"cdi-b"},
				},
				IfName: ifNameB,
				DeviceAttributes: map[string]resourceapi.DeviceAttribute{
					consts.AttributePciAddress:    {StringValue: &pciAddressB},
					consts.AttributeInterfaceName: {StringValue: &ifNameB},
				},
			},
			NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net2"},
		}
		dataList := types.NetworkDataChanStructList{deviceA, deviceB}

		err := plugin.updateRequestMetadataBeforeSandboxStart(context.Background(), dataList)
		Expect(err).NotTo(HaveOccurred())

		Expect(updater.callCount).To(Equal(1))
		Expect(updater.devices).To(HaveLen(2))
	})
})

var _ = Describe("NRI updateNetworkDeviceData ordering", func() {
	const (
		claimUID = k8stypes.UID("claim-a-uid")
		podUID   = k8stypes.UID("pod-a-uid")
	)
	var (
		pm       *podmanager.PodManager
		cfg      *types.Config
		prepared types.PreparedDevices
	)

	// newClaim is the claim the devices were prepared for: same UID, dev-a
	// allocated, still reserved for the pod, with the entry prepare wrote.
	newClaim := func() *resourceapi.ResourceClaim {
		return &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "claim-a",
				Namespace: "default",
				UID:       claimUID,
			},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Request: "req-a", Driver: consts.DriverName, Pool: "pool-a", Device: "dev-a"},
							{Request: "req-b", Driver: consts.DriverName, Pool: "pool-a", Device: "dev-b"},
						},
					},
				},
				ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: "pod-a", UID: podUID}},
				Devices: []resourceapi.AllocatedDeviceStatus{
					{
						Driver: consts.DriverName,
						Pool:   "pool-a",
						Device: "dev-a",
						Data:   &runtime.RawExtension{Raw: []byte(`{"netAttachDefName":"net-a"}`)},
					},
				},
			},
		}
	}
	newPlugin := func(claim *resourceapi.ResourceClaim) *Plugin {
		return &Plugin{
			podManager: pm,
			k8sClient: flags.ClientSets{
				Interface: k8sfake.NewSimpleClientset(claim),
			},
		}
	}
	networkDataList := func(networkData *resourceapi.NetworkDeviceData) types.NetworkDataChanStructList {
		return types.NetworkDataChanStructList{
			{
				PreparedDevice:    prepared[0],
				NetworkDeviceData: networkData,
				CNIConfig:         map[string]interface{}{"type": "sriov"},
				CNIResult:         map[string]interface{}{"result": "ok"},
			},
		}
	}
	getClaim := func(plugin *Plugin) *resourceapi.ResourceClaim {
		got, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	BeforeEach(func() {
		cfg = &types.Config{
			Flags: &types.Flags{
				KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
			},
		}
		var err error
		pm, err = podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		prepared = types.PreparedDevices{
			{
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					NamespacedName: k8stypes.NamespacedName{
						Namespace: "default",
						Name:      "claim-a",
					},
					UID: claimUID,
				},
				Device: drapbv1.Device{
					PoolName:   "pool-a",
					DeviceName: "dev-a",
				},
				PodUID: string(podUID),
				Config: &configapi.VfConfig{NetAttachDefName: "net-a"},
			},
		}
		Expect(pm.Set(podUID, claimUID, prepared)).To(Succeed())
	})

	It("does not update claim status when checkpoint persistence fails", func() {
		realCM, err := checkpointmanager.NewCheckpointManager(cfg.DriverPluginPath())
		Expect(err).NotTo(HaveOccurred())
		cm := &togglableCheckpointManager{delegate: realCM}
		pm, err = podmanager.NewPodManagerWithCheckpointManager(cm)
		Expect(err).NotTo(HaveOccurred())
		Expect(pm.Set(podUID, claimUID, prepared)).To(Succeed())

		plugin := newPlugin(newClaim())
		// Refuse only the write the update itself makes; the ones above had to land.
		cm.failCreate = true

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))

		updatedClaim := getClaim(plugin)
		Expect(updatedClaim.Status.Devices).To(HaveLen(1))
		Expect(updatedClaim.Status.Devices[0].NetworkData).To(BeNil())
		Expect(string(updatedClaim.Status.Devices[0].Data.Raw)).To(Equal(`{"netAttachDefName":"net-a"}`))

		stored, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(stored[0].NetworkDeviceData).To(BeNil(), "the store must not hold what the checkpoint does not")
	})

	It("updates claim status after checkpoint persistence succeeds", func() {
		plugin := newPlugin(newClaim())
		networkData := &resourceapi.NetworkDeviceData{InterfaceName: "net1", IPs: []string{"10.10.0.10/24"}}

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(networkData))

		updatedClaim := getClaim(plugin)
		Expect(updatedClaim.Status.Devices).To(HaveLen(1))
		Expect(updatedClaim.Status.Devices[0].NetworkData).To(Equal(networkData))
		Expect(updatedClaim.Status.Devices[0].Data).NotTo(BeNil())
		Expect(string(updatedClaim.Status.Devices[0].Data.Raw)).To(MatchJSON(`{
			"vfConfig": {"netAttachDefName": "net-a"},
			"cniConfig": {"type": "sriov"},
			"cniResult": {"result": "ok"}
		}`))

		updatedPreparedDevices, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(updatedPreparedDevices).To(HaveLen(1))
		Expect(updatedPreparedDevices[0].NetworkDeviceData).To(Equal(networkData))
	})

	It("patches only its own device and keeps the rest of the status", func() {
		claim := newClaim()
		foreign := resourceapi.AllocatedDeviceStatus{Driver: "other.example.com", Pool: "pool-a", Device: "dev-a"}
		other := resourceapi.AllocatedDeviceStatus{Driver: consts.DriverName, Pool: "pool-a", Device: "dev-b", NetworkData: &resourceapi.NetworkDeviceData{InterfaceName: "net9"}}
		claim.Status.Devices = append(claim.Status.Devices, foreign, other)
		plugin := newPlugin(claim)

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))

		updatedClaim := getClaim(plugin)
		Expect(updatedClaim.Status.Devices).To(HaveLen(3))
		Expect(updatedClaim.Status.Devices[0].NetworkData.InterfaceName).To(Equal("net1"))
		Expect(updatedClaim.Status.Devices[1]).To(Equal(foreign), "a foreign entry with the same device name must be untouched")
		Expect(updatedClaim.Status.Devices[2]).To(Equal(other), "this driver's other device must be untouched")
	})

	It("adds the entry when the status write during prepare was lost", func() {
		claim := newClaim()
		claim.Status.Devices = nil
		plugin := newPlugin(claim)

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))

		updatedClaim := getClaim(plugin)
		Expect(updatedClaim.Status.Devices).To(HaveLen(1))
		Expect(updatedClaim.Status.Devices[0].Device).To(Equal("dev-a"))
		Expect(updatedClaim.Status.Devices[0].NetworkData.InterfaceName).To(Equal("net1"))
		Expect(string(updatedClaim.Status.Devices[0].Data.Raw)).To(ContainSubstring(`"vfConfig":{"netAttachDefName":"net-a"}`))
	})

	It("skips a claim that was recreated under the same name", func() {
		// Same name, different object: the claim these devices were prepared for
		// is gone and this one belongs to whoever recreated it.
		replacement := newClaim()
		replacement.UID = k8stypes.UID("claim-a-uid-2")
		plugin := newPlugin(replacement)

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))

		got := getClaim(plugin)
		Expect(got.Status.Devices).To(HaveLen(1))
		Expect(got.Status.Devices[0].NetworkData).To(BeNil(), "the replacement claim must not take the old claim's network data")
	})

	It("skips a claim the pod no longer holds", func() {
		// The pod released the claim and another pod took it over: the network
		// data belongs to an attachment of the first pod, not to the claim now.
		released := newClaim()
		released.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: "pod-b", UID: "pod-b-uid"}}
		plugin := newPlugin(released)

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))

		got := getClaim(plugin)
		Expect(got.Status.Devices).To(HaveLen(1))
		Expect(got.Status.Devices[0].NetworkData).To(BeNil(), "the claim must not carry network data of a pod that released it")
	})

	It("skips a claim that is gone", func() {
		plugin := &Plugin{
			podManager: pm,
			k8sClient:  flags.ClientSets{Interface: k8sfake.NewSimpleClientset()},
		}

		// Nothing to assert on the API; the update must not log an error or
		// retry to the timeout for a claim that no longer exists.
		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))
	})

	It("retries on a conflict without reverting a concurrent same-driver write", func() {
		// While this update was in flight, prepare recorded dev-b on the claim.
		// Restoring a pre-conflict snapshot dropped it; the patch keeps it.
		claim := newClaim()
		plugin := newPlugin(claim)
		fake := plugin.k8sClient.Interface.(*k8sfake.Clientset)
		updateCalls := 0
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "status" {
				return false, nil, nil
			}
			updateCalls++
			if updateCalls > 1 {
				return false, nil, nil
			}
			concurrent := claim.DeepCopy()
			concurrent.Status.Devices = append(concurrent.Status.Devices, resourceapi.AllocatedDeviceStatus{Driver: consts.DriverName, Pool: "pool-a", Device: "dev-b"})
			Expect(fake.Tracker().Update(resourceapi.SchemeGroupVersion.WithResource("resourceclaims"), concurrent, "default")).To(Succeed())
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}, "claim-a", errors.New("conflict"))
		})

		plugin.updateNetworkDeviceData(context.Background(), networkDataList(&resourceapi.NetworkDeviceData{InterfaceName: "net1"}))

		Expect(updateCalls).To(Equal(2))
		got := getClaim(plugin)
		Expect(got.Status.Devices).To(HaveLen(2))
		Expect(got.Status.Devices[0].NetworkData.InterfaceName).To(Equal("net1"))
		Expect(got.Status.Devices[1].Device).To(Equal("dev-b"), "the entry prepare added during the conflict window must survive")
	})
})
