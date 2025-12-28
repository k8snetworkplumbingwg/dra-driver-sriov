package nri

import (
	"context"
	"errors"

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
})

// No stub needed for unit tests; we do not call Start/Stop on the plugin
