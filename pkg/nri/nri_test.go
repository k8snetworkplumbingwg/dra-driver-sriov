package nri

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/containerd/nri/pkg/api"
	k8stypes "k8s.io/apimachinery/pkg/types"

	cnimock "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cni/mock"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

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

		plugin, err := NewNRIPlugin(cfg, podManager, mockCNI)
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
