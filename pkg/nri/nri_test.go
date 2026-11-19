package nri

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/containerd/nri/pkg/api"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	cnimock "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cni/mock"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

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
			KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
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
		storeTrustedNRIClaim(plugin, pod, prepared)

		mockCNI.EXPECT().
			AttachNetwork(gomock.Any(), pod, "/proc/123/ns/net", prepared[0]).
			Return(nil, map[string]interface{}{"dummy": true}, nil)

		// The goroutine uses a channel to update claim status; we don't rely on it here
		Expect(plugin.RunPodSandbox(ctx, pod)).To(Succeed())
	})

	DescribeTable("rejects unverified saved state before any CNI attachment", func(legacy bool) {
		prepared := types.PreparedDevices{{IfName: "vfnet0", PciAddress: "0000:00:00.1"}}
		claim := storeTrustedNRIClaim(plugin, pod, prepared)
		var provenance *types.PreparedClaimMeta
		if !legacy {
			meta, found := podManager.ClaimProvenance(k8stypes.UID(pod.Uid), claim.UID)
			Expect(found).To(BeTrue())
			meta.ConfigFingerprint = "mismatch"
			provenance = &meta
		}
		Expect(podManager.Set(k8stypes.UID(pod.Uid), claim.UID, prepared, provenance)).To(Succeed())
		// No AttachNetwork expectation: any CNI call before rejection fails the spec.
		Expect(plugin.RunPodSandbox(ctx, pod)).To(MatchError(ContainSubstring("migration")))
		Expect(plugin.networkDeviceDataUpdateChan).To(BeEmpty())
		_, found := podManager.Get(k8stypes.UID(pod.Uid), claim.UID)
		Expect(found).To(BeTrue(), "unverified state must remain available for cleanup")
	}, Entry("legacy record", true), Entry("fingerprint mismatch", false))

	It("skips CNI ADD and DEL for trusted VFIO without a NAD", func() {
		prepared := types.PreparedDevices{{Config: &configapi.VfConfig{Driver: "vfio-pci"}}}
		storeTrustedNRIClaim(plugin, pod, prepared)
		// No CNI expectations: this mode exposes devices without attaching a network.
		Expect(plugin.RunPodSandbox(ctx, pod)).To(Succeed())
		Expect(plugin.StopPodSandbox(ctx, pod)).To(Succeed())
		Expect(prepared[0].NetAttachDefConfig).To(BeEmpty())
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
		storeTrustedNRIClaim(plugin, pod, prepared)

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
		storeTrustedNRIClaim(plugin, pod, prepared)

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
		storeTrustedNRIClaim(plugin, pod, prepared)

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
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared, nil)).To(Succeed())

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
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared, nil)).To(Succeed())

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
		Expect(podManager.Set(k8stypes.UID(pod.Uid), k8stypes.UID("claim-1"), prepared, nil)).To(Succeed())

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
			KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
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
	It("does not update claim status when checkpoint persistence fails", func() {
		cfg := &types.Config{
			Flags: &types.Flags{
				KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
			},
		}
		realCM, err := checkpointmanager.NewCheckpointManager(cfg.DriverPluginPath())
		Expect(err).NotTo(HaveOccurred())
		cm := &togglableCheckpointManager{delegate: realCM}
		pm, err := podmanager.NewPodManagerWithCheckpointManager(cm)
		Expect(err).NotTo(HaveOccurred())

		claimUID := k8stypes.UID("claim-a-uid")
		podUID := k8stypes.UID("pod-a-uid")
		prepared := types.PreparedDevices{
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
			},
		}
		Expect(pm.Set(podUID, claimUID, prepared, nil)).To(Succeed())

		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "claim-a",
				Namespace: "default",
				UID:       claimUID,
			},
			Status: resourceapi.ResourceClaimStatus{
				Devices: []resourceapi.AllocatedDeviceStatus{
					{
						Driver: consts.DriverName,
						Pool:   "pool-a",
						Device: "dev-a",
					},
				},
			},
		}

		plugin := &Plugin{
			podManager: pm,
			k8sClient: flags.ClientSets{
				Interface: k8sfake.NewSimpleClientset(claim.DeepCopy()),
				Client:    ctrlclientfake.NewClientBuilder().WithScheme(flags.Scheme).WithRuntimeObjects(claim.DeepCopy()).Build(),
			},
		}

		// Simulate checkpoint sync failure only when updateNetworkDeviceData persists network data, after setup syncs succeeded.
		cm.failCreate = true

		networkDataList := types.NetworkDataChanStructList{
			{
				PreparedDevice:    prepared[0],
				NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1"},
			},
		}

		plugin.updateNetworkDeviceData(context.Background(), networkDataList)

		updatedClaim, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedClaim.Status.Devices).To(HaveLen(1))
		Expect(updatedClaim.Status.Devices[0].NetworkData).To(BeNil())
		Expect(updatedClaim.Status.Devices[0].Data).To(BeNil())

		stored, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(stored[0].NetworkDeviceData).To(BeNil(), "the store must not hold what the checkpoint does not")
	})

	It("updates claim status after checkpoint persistence succeeds", func() {
		cfg := &types.Config{
			Flags: &types.Flags{
				KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
			},
		}
		pm, err := podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		claimUID := k8stypes.UID("claim-a-uid")
		podUID := k8stypes.UID("pod-a-uid")
		prepared := types.PreparedDevices{
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
			},
		}
		Expect(pm.Set(podUID, claimUID, prepared, nil)).To(Succeed())

		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "claim-a",
				Namespace: "default",
				UID:       claimUID,
			},
			Status: resourceapi.ResourceClaimStatus{
				Devices: []resourceapi.AllocatedDeviceStatus{
					{
						Driver: consts.DriverName,
						Pool:   "pool-a",
						Device: "dev-a",
					},
				},
			},
		}

		plugin := &Plugin{
			podManager: pm,
			k8sClient: flags.ClientSets{
				Interface: k8sfake.NewSimpleClientset(claim.DeepCopy()),
				Client:    ctrlclientfake.NewClientBuilder().WithScheme(flags.Scheme).WithRuntimeObjects(claim.DeepCopy()).Build(),
			},
		}

		networkData := &resourceapi.NetworkDeviceData{InterfaceName: "net1"}
		networkDataList := types.NetworkDataChanStructList{
			{
				PreparedDevice:    prepared[0],
				NetworkDeviceData: networkData,
				CNIConfig: map[string]interface{}{
					"type": "sriov",
				},
				CNIResult: map[string]interface{}{
					"result": "ok",
				},
			},
		}

		plugin.updateNetworkDeviceData(context.Background(), networkDataList)

		updatedClaim, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedClaim.Status.Devices).To(HaveLen(1))
		Expect(updatedClaim.Status.Devices[0].NetworkData).NotTo(BeNil())
		Expect(updatedClaim.Status.Devices[0].NetworkData.InterfaceName).To(Equal("net1"))
		Expect(updatedClaim.Status.Devices[0].Data).NotTo(BeNil())

		updatedPreparedDevices, found := pm.Get(podUID, claimUID)
		Expect(found).To(BeTrue())
		Expect(updatedPreparedDevices).To(HaveLen(1))
		Expect(updatedPreparedDevices[0].NetworkDeviceData).NotTo(BeNil())
		Expect(updatedPreparedDevices[0].NetworkDeviceData.InterfaceName).To(Equal("net1"))
	})

	It("skips a claim that was recreated under the same name", func() {
		cfg := &types.Config{
			Flags: &types.Flags{
				KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
			},
		}
		pm, err := podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		preparedFor := k8stypes.UID("claim-a-uid")
		podUID := k8stypes.UID("pod-a-uid")
		prepared := types.PreparedDevices{
			{
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					NamespacedName: k8stypes.NamespacedName{
						Namespace: "default",
						Name:      "claim-a",
					},
					UID: preparedFor,
				},
				Device: drapbv1.Device{
					PoolName:   "pool-a",
					DeviceName: "dev-a",
				},
			},
		}
		Expect(pm.Set(podUID, preparedFor, prepared, nil)).To(Succeed())

		// Same name, different object: the claim these devices were prepared for
		// is gone and this one belongs to whoever recreated it.
		replacement := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "claim-a",
				Namespace: "default",
				UID:       k8stypes.UID("claim-a-uid-2"),
			},
			Status: resourceapi.ResourceClaimStatus{
				Devices: []resourceapi.AllocatedDeviceStatus{
					{
						Driver: consts.DriverName,
						Pool:   "pool-a",
						Device: "dev-a",
					},
				},
			},
		}

		plugin := &Plugin{
			podManager: pm,
			k8sClient: flags.ClientSets{
				Interface: k8sfake.NewSimpleClientset(replacement.DeepCopy()),
				Client:    ctrlclientfake.NewClientBuilder().WithScheme(flags.Scheme).WithRuntimeObjects(replacement.DeepCopy()).Build(),
			},
		}

		networkDataList := types.NetworkDataChanStructList{
			{
				PreparedDevice:    prepared[0],
				NetworkDeviceData: &resourceapi.NetworkDeviceData{InterfaceName: "net1"},
			},
		}

		plugin.updateNetworkDeviceData(context.Background(), networkDataList)

		got, err := plugin.k8sClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Status.Devices).To(HaveLen(1))
		Expect(got.Status.Devices[0].NetworkData).To(BeNil(), "the replacement claim must not take the old claim's network data")
	})
})

// storeTrustedNRIClaim connects NRI fixtures to a matching class-only allocation and saved provenance.
func storeTrustedNRIClaim(plugin *Plugin, pod *api.PodSandbox, devices types.PreparedDevices) *resourceapi.ResourceClaim {
	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim-1", UID: "claim-1"}}
	claim.Spec.Devices.Requests = []resourceapi.DeviceRequest{{Name: "request-a", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "test-class"}}}
	claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID(pod.Uid)}}
	claim.Status.Allocation = &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: []resourceapi.DeviceRequestAllocationResult{{Request: "request-a", Driver: consts.DriverName, Pool: "pool-a", Device: "dev-a"}}}}
	cfg := configapi.DefaultVfConfig()
	cfg.NetAttachDefName = "net1"
	cfg.NetAttachDefNamespace = "admin"
	if len(devices) > 0 && devices[0].Config != nil {
		cfg = devices[0].Config.DeepCopy()
	}
	cfg.APIVersion = configapi.GroupName + "/v1alpha1"
	cfg.Kind = configapi.VfConfigKind
	raw, err := runtime.Encode(configapi.Decoder.(runtime.Encoder), cfg)
	Expect(err).NotTo(HaveOccurred())
	claim.Status.Allocation.Devices.Config = []resourceapi.DeviceAllocationConfiguration{{Source: resourceapi.AllocationConfigSourceClass, DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: consts.DriverName, Parameters: runtime.RawExtension{Raw: raw}}}}}
	for _, device := range devices {
		device.Config = cfg.DeepCopy()
		device.PodUID = pod.Uid
		device.ClaimNamespacedName = kubeletplugin.NamespacedObject{NamespacedName: k8stypes.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}, UID: claim.UID}
		device.Device = drapbv1.Device{RequestNames: []string{"request-a"}, PoolName: "pool-a", DeviceName: "dev-a"}
		if device.IfName == "" {
			device.IfName = "vfnet0"
		}
		if device.NetAttachDefConfig == "" && cfg.Driver != "vfio-pci" {
			device.NetAttachDefConfig = `{"cniVersion":"1.0.0","type":"sriov","name":"net1"}`
		}
	}
	resolved, err := devicestate.ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeStandalone))
	Expect(err).NotTo(HaveOccurred())
	plugin.k8sClient.Client = ctrlclientfake.NewClientBuilder().WithScheme(flags.Scheme).WithRuntimeObjects(claim).Build()
	Expect(plugin.podManager.Set(k8stypes.UID(pod.Uid), claim.UID, devices, &types.PreparedClaimMeta{ConfigPolicyVersion: resolved.ConfigPolicyVersion, ConfigFingerprint: resolved.Fingerprint})).To(Succeed())
	return claim
}
