package driver

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	mock_host "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// The prepare path end to end, with the host mocked: discovery finds one PF
// with two VFs, and each claim allocates one of them.
var _ = Describe("PrepareResourceClaims status write", func() {
	const (
		namespace = "vf-test"
		pfAddress = "0000:01:00.0"
		podUID    = k8stypes.UID("pod-uid")
	)
	vfs := []host.VFInfo{
		{PciAddress: "0000:01:00.1", VFID: 0, DeviceID: "154c"},
		{PciAddress: "0000:01:00.2", VFID: 1, DeviceID: "154c"},
	}
	deviceNames := []string{"0000-01-00-1", "0000-01-00-2"}
	gr := schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}

	var (
		mockCtrl    *gomock.Controller
		mockHost    *mock_host.MockInterface
		origHelpers host.Interface
		origTimeout time.Duration
		fake        *k8sfake.Clientset
		d           *Driver
	)

	newClaim := func(name, device string) *resourceapi.ResourceClaim {
		return &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: k8stypes.UID(name + "-uid")},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Request: "vf", Driver: consts.DriverName, Pool: "pool-a", Device: device},
						},
					},
				},
				ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: "pod", UID: podUID}},
			},
		}
	}
	// statusUpdates counts the status updates per claim and lets a test fail
	// them all with err.
	statusUpdates := func(fail error) map[string]*atomic.Int32 {
		calls := map[string]*atomic.Int32{}
		fake.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "status" {
				return false, nil, nil
			}
			name := action.(k8stesting.UpdateAction).GetObject().(*resourceapi.ResourceClaim).Name
			if calls[name] == nil {
				calls[name] = &atomic.Int32{}
			}
			calls[name].Add(1)
			if fail != nil {
				return true, nil, fail
			}
			return false, nil, nil
		})
		return calls
	}
	getClaim := func(name string) *resourceapi.ResourceClaim {
		got, err := fake.ResourceV1().ResourceClaims(namespace).Get(context.Background(), name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockHost = mock_host.NewMockInterface(mockCtrl)
		_ = host.GetHelpers()
		origHelpers = host.Helpers
		host.Helpers = mockHost
		origTimeout = prepareStatusTimeout

		// Discovery, run once by NewManager.
		mockHost.EXPECT().PCI().Return(&pci.Info{Devices: []*pci.Device{{
			Address: pfAddress,
			Class:   &pcidb.Class{ID: "02"},
			Vendor:  &pcidb.Vendor{ID: "8086"},
			Product: &pcidb.Product{ID: "1572"},
		}}}, nil)
		mockHost.EXPECT().IsSriovVF(pfAddress).Return(false)
		mockHost.EXPECT().TryGetPFInterfaceName(pfAddress).Return("eth0")
		mockHost.EXPECT().GetNicSriovMode(pfAddress).Return(consts.EswitchModeLegacy)
		mockHost.EXPECT().GetNumaNode(pfAddress).Return("0", nil)
		mockHost.EXPECT().GetPCIeRoot(pfAddress).Return("pci0000:00", nil)
		mockHost.EXPECT().GetLinkType(pfAddress).Return(consts.LinkTypeEthernet, nil)
		mockHost.EXPECT().GetVFList(pfAddress).Return(vfs, nil)
		for _, vf := range vfs {
			mockHost.EXPECT().VerifyRDMACapability(vf.PciAddress).Return(false)
		}
		mockHost.EXPECT().IsIommufdAvailable().Return(false)
		// Prepare binds each VF with the default (empty) driver config.
		mockHost.EXPECT().BindDeviceDriver(gomock.Any(), gomock.Any()).Return("", nil).AnyTimes()

		// MULTUS mode skips the NetworkAttachmentDefinition lookup, which is
		// the only API access on this path besides the status write.
		cfg := &types.Config{Flags: &types.Flags{
			ConfigurationMode:           string(consts.ConfigurationModeMultus),
			KubeletPluginsDirectoryPath: GinkgoT().TempDir(),
			DefaultInterfacePrefix:      "net",
		}}
		cdiHandler, err := cdi.NewHandler(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		manager, err := devicestate.NewManager(cfg, cdiHandler, nil)
		Expect(err).NotTo(HaveOccurred())
		pm, err := podmanager.NewPodManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		fake = k8sfake.NewSimpleClientset(newClaim("claim-a", deviceNames[0]), newClaim("claim-b", deviceNames[1]))
		d = &Driver{client: fake, deviceStateManager: manager, podManager: pm, cdi: cdiHandler, config: cfg}
	})

	AfterEach(func() {
		host.Helpers = origHelpers
		prepareStatusTimeout = origTimeout
		mockCtrl.Finish()
	})

	It("records the applied configuration on the claim", func() {
		calls := statusUpdates(nil)

		result, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{newClaim("claim-a", deviceNames[0])})
		Expect(err).NotTo(HaveOccurred())
		Expect(result["claim-a-uid"].Err).NotTo(HaveOccurred())
		Expect(result["claim-a-uid"].Devices).To(HaveLen(1))
		Expect(calls["claim-a"].Load()).To(Equal(int32(1)))

		devices := getClaim("claim-a").Status.Devices
		Expect(devices).To(HaveLen(1))
		Expect(devices[0].Driver).To(Equal(consts.DriverName))
		Expect(devices[0].Pool).To(Equal("pool-a"))
		Expect(devices[0].Device).To(Equal(deviceNames[0]))
		Expect(devices[0].Data).NotTo(BeNil())
		Expect(string(devices[0].Data.Raw)).To(ContainSubstring(`"kind":"VfConfig"`))
		Expect(devices[0].NetworkData).To(BeNil())
	})

	It("still returns the prepared devices when the status write fails", func() {
		// The write is best-effort: the devices are prepared and recorded in
		// the pod manager, so the kubelet gets them.
		calls := statusUpdates(apierrors.NewForbidden(gr, "claim-a", errors.New("denied")))

		result, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{newClaim("claim-a", deviceNames[0])})
		Expect(err).NotTo(HaveOccurred())
		Expect(result["claim-a-uid"].Err).NotTo(HaveOccurred())
		Expect(result["claim-a-uid"].Devices).To(HaveLen(1))
		Expect(calls["claim-a"].Load()).To(Equal(int32(1)), "a permanent error is not retried")
		Expect(getClaim("claim-a").Status.Devices).To(BeEmpty())
		_, found := d.podManager.Get(podUID, "claim-a-uid")
		Expect(found).To(BeTrue())
	})

	It("shares one status-write budget across the claims of a call", func() {
		// The API keeps timing out. The first claim's retries use up the budget,
		// so the second claim's write is not even attempted; with a budget per
		// claim the call would spend twice as long against the kubelet's
		// deadline for the whole call.
		prepareStatusTimeout = 300 * time.Millisecond
		calls := statusUpdates(apierrors.NewServerTimeout(gr, "update", 1))

		start := time.Now()
		result, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{
			newClaim("claim-a", deviceNames[0]),
			newClaim("claim-b", deviceNames[1]),
		})
		elapsed := time.Since(start)
		Expect(err).NotTo(HaveOccurred())
		Expect(result["claim-a-uid"].Devices).To(HaveLen(1))
		Expect(result["claim-b-uid"].Devices).To(HaveLen(1))
		Expect(calls["claim-a"].Load()).To(BeNumerically(">=", 1))
		Expect(calls["claim-b"]).To(BeNil(), "the budget was spent on claim-a, so claim-b's write must not start")
		Expect(elapsed).To(BeNumerically("<", 2*prepareStatusTimeout))
	})
})
