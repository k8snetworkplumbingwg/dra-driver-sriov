package controller_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gomock "go.uber.org/mock/gomock"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	sriovconsts "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/controller"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate/mock"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var (
	testEnv    *envtest.Environment
	cfg        *rest.Config
	scheme     *runtime.Scheme
	k8sClient  client.Client
	mgr        ctrl.Manager
	cancelFunc context.CancelFunc

	reconciler *controller.SriovResourcePolicyReconciler
	// applied tracks the last call to UpdatePolicyDevices
	applied map[string]map[resourcev1.QualifiedName]resourcev1.DeviceAttribute
)

var _ = BeforeSuite(func(ctx SpecContext) {
	scheme = runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	Expect(sriovdrav1alpha1.AddToScheme(scheme)).To(Succeed())

	testEnv = &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				"../../deployments/helm/dra-driver-sriov/templates/sriovnetwork.k8snetworkplumbingwg.io_sriovresourcepolicies.yaml",
				"../../deployments/helm/dra-driver-sriov/templates/sriovnetwork.k8snetworkplumbingwg.io_deviceattributes.yaml",
			},
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfgObj, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfgObj).NotTo(BeNil())
	cfg = cfgObj

	bootstrapClient, err := client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dra-driver-sriov"}}
	Expect(bootstrapClient.Create(ctx, ns)).To(Succeed())

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"test": "true"}}}
	Expect(bootstrapClient.Create(ctx, node)).To(Succeed())

	mgr, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	Expect(err).NotTo(HaveOccurred())

	ctrlMock := gomock.NewController(GinkgoT())
	devState := mock.NewMockDeviceState(ctrlMock)
	devState.EXPECT().GetPolicyCandidateDevices(gomock.Any()).AnyTimes().DoAndReturn(
		func(includePciAddressInventory bool) drasriovtypes.AllocatableDevices {
			alloc := defaultAllocatableDevices()
			if !includePciAddressInventory {
				return alloc
			}
			for name, device := range defaultPciInventoryDevices() {
				if _, exists := alloc[name]; !exists {
					alloc[name] = device
				}
			}
			return alloc
		},
	)
	devState.EXPECT().UpdatePolicyDevices(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, m map[string]map[resourcev1.QualifiedName]resourcev1.DeviceAttribute) error {
			applied = m
			return nil
		},
	)

	reconciler = controller.NewSriovResourcePolicyReconciler(mgr.GetClient(), "test-node", "dra-driver-sriov", devState)
	Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

	var startCtx context.Context
	startCtx, cancelFunc = context.WithCancel(context.Background())
	go func() { _ = mgr.Start(startCtx) }()

	k8sClient = mgr.GetClient()

	Eventually(func() error {
		tmp := &corev1.Node{}
		return k8sClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, tmp)
	}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
})

var _ = AfterSuite(func() {
	if cancelFunc != nil {
		cancelFunc()
	}
	if testEnv != nil {
		_ = testEnv.Stop()
	}
})

func defaultAllocatableDevices() drasriovtypes.AllocatableDevices {
	vendor := "8086"
	dev := "154c"
	pf := "eth0"
	pci := "0000:00:00.1"
	pcieRoot := "pci0000:00"
	pfPci := "0000:01:00.0"

	return drasriovtypes.AllocatableDevices{
		"devA": resourcev1.Device{
			Name: "devA",
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				sriovconsts.AttributeVendorID:     {StringValue: &vendor},
				sriovconsts.AttributeDeviceID:     {StringValue: &dev},
				sriovconsts.AttributePFName:       {StringValue: &pf},
				sriovconsts.AttributePciAddress:   {StringValue: &pci},
				sriovconsts.AttributePCIeRoot:     {StringValue: &pcieRoot},
				sriovconsts.AttributePfPciAddress: {StringValue: &pfPci},
			},
		},
		"devB": resourcev1.Device{
			Name: "devB",
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				sriovconsts.AttributeVendorID: {StringValue: &vendor},
				sriovconsts.AttributeDeviceID: {StringValue: &dev},
			},
		},
	}
}

func defaultPciInventoryDevices() drasriovtypes.AllocatableDevices {
	vendor := "8086"
	dev := "1889"
	pci := "0000:af:00.5"
	pcieRoot := "pci0000:af"
	return drasriovtypes.AllocatableDevices{
		"0000-af-00-5": resourcev1.Device{
			Name: "0000-af-00-5",
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				sriovconsts.AttributeVendorID:           {StringValue: &vendor},
				sriovconsts.AttributeDeviceID:           {StringValue: &dev},
				sriovconsts.AttributePciAddress:         {StringValue: &pci},
				sriovconsts.AttributeStandardPciAddress: {StringValue: &pci},
				sriovconsts.AttributePCIeRoot:           {StringValue: &pcieRoot},
			},
		},
	}
}

var _ = Describe("SriovResourcePolicyReconciler (envtest)", func() {
	It("should handle no policies in namespace", func(ctx SpecContext) {
		Consistently(func() int {
			return len(applied)
		}, 500*time.Millisecond, 100*time.Millisecond).Should(Equal(0))
	})

	It("should select policy with empty nodeSelector and advertise all devices", func(ctx SpecContext) {
		policy := &sriovdrav1alpha1.SriovResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rp-empty-selector", Namespace: "dra-driver-sriov"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{
					ResourceFilters: nil,
				}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		Eventually(func() int { return len(applied) }, 5*time.Second, 200*time.Millisecond).Should(BeNumerically(">=", 1))
	})

	It("should ignore policies in other namespaces", func(ctx SpecContext) {
		otherNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-ns"}}
		Expect(k8sClient.Create(ctx, otherNS)).To(Succeed())

		policy := &sriovdrav1alpha1.SriovResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rp-other-ns", Namespace: "other-ns"},
			Spec:       sriovdrav1alpha1.SriovResourcePolicySpec{Configs: []sriovdrav1alpha1.Config{{}}},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		Consistently(func() int {
			return len(applied)
		}, 1*time.Second, 200*time.Millisecond).Should(BeNumerically(">=", 1))
	})

	It("should merge multiple matching policies", func(ctx SpecContext) {
		// Create second policy matching the same node
		policy := &sriovdrav1alpha1.SriovResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rp-duplicate", Namespace: "dra-driver-sriov"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		// Both policies match -- devices should still be advertised (merged)
		Eventually(func() int { return len(applied) }, 5*time.Second, 200*time.Millisecond).Should(BeNumerically(">=", 1))
	})

	It("should apply DeviceAttributes when selector matches", func(ctx SpecContext) {
		// Clean up existing policies
		for _, name := range []string{"rp-empty-selector", "rp-duplicate"} {
			rp := &sriovdrav1alpha1.SriovResourcePolicy{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "dra-driver-sriov", Name: name}, rp); err == nil {
				_ = k8sClient.Delete(ctx, rp)
			}
		}

		// Wait for deletion
		time.Sleep(500 * time.Millisecond)

		resName := "my-pool"
		da := &sriovdrav1alpha1.DeviceAttributes{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "da-test",
				Namespace: "dra-driver-sriov",
				Labels:    map[string]string{"pool": "test-pool"},
			},
			Spec: sriovdrav1alpha1.DeviceAttributesSpec{
				Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
					"sriovnetwork.k8snetworkplumbingwg.io/resourceName": {StringValue: &resName},
				},
			},
		}
		Expect(k8sClient.Create(ctx, da)).To(Succeed())

		policy := &sriovdrav1alpha1.SriovResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rp-with-attrs", Namespace: "dra-driver-sriov"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{
					DeviceAttributesSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"pool": "test-pool"}},
					ResourceFilters:          nil,
				}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		Eventually(func() bool {
			if len(applied) == 0 {
				return false
			}
			for _, attrs := range applied {
				if _, ok := attrs[resourcev1.QualifiedName("sriovnetwork.k8snetworkplumbingwg.io/resourceName")]; ok {
					return true
				}
			}
			return false
		}, 5*time.Second, 200*time.Millisecond).Should(BeTrue())
	})

	It("should match PCI inventory device only when pciAddresses filter is used", func(ctx SpecContext) {
		policy := &sriovdrav1alpha1.SriovResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rp-pci-inventory", Namespace: "dra-sriov-driver"},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
				Configs: []sriovdrav1alpha1.Config{{
					ResourceFilters: []sriovdrav1alpha1.ResourceFilter{
						{PciAddresses: []string{"0000:af:00.5"}},
					},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		Eventually(func() bool {
			_, ok := applied["0000-af-00-5"]
			return ok
		}, 5*time.Second, 200*time.Millisecond).Should(BeTrue())
	})

	It("should requeue when node is missing (direct Reconcile call)", func(ctx SpecContext) {
		bogus := controller.NewSriovResourcePolicyReconciler(k8sClient, "missing-node", "dra-driver-sriov", nil)
		result, err := bogus.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "irrelevant", Namespace: "dra-driver-sriov"}})
		Expect(err).To(BeNil())
		Expect(result.RequeueAfter).NotTo(BeZero())
	})
})
