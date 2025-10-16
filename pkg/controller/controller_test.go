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

	sriovdrav1alpha1 "github.com/SchSeba/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	sriovconsts "github.com/SchSeba/dra-driver-sriov/pkg/consts"
	"github.com/SchSeba/dra-driver-sriov/pkg/controller"
	"github.com/SchSeba/dra-driver-sriov/pkg/devicestate/mock"
	drasriovtypes "github.com/SchSeba/dra-driver-sriov/pkg/types"
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

	reconciler *controller.SriovResourceFilterReconciler
	applied    map[string]string
)

var _ = BeforeSuite(func(ctx SpecContext) {
	scheme = runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	Expect(sriovdrav1alpha1.AddToScheme(scheme)).To(Succeed())

	testEnv = &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				"../../deployments/helm/dra-driver-sriov/templates/sriovnetwork.openshift.io_sriovresourcefilters.yaml",
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

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dra-sriov-driver"}}
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
	devState.EXPECT().GetAllocatableDevices().AnyTimes().Return(defaultAllocatableDevices())
	devState.EXPECT().UpdateDeviceResourceNames(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, m map[string]string) error { applied = m; return nil },
	)

	reconciler = controller.NewSriovResourceFilterReconciler(mgr.GetClient(), "test-node", "dra-sriov-driver", devState)
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
	root := "0000:00:00.0"
	numa := int64(0)

	return drasriovtypes.AllocatableDevices{
		"devA": resourcev1.Device{
			Name: "devA",
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				sriovconsts.AttributeVendorID:         {StringValue: &vendor},
				sriovconsts.AttributeDeviceID:         {StringValue: &dev},
				sriovconsts.AttributePFName:           {StringValue: &pf},
				sriovconsts.AttributePciAddress:       {StringValue: &pci},
				sriovconsts.AttributeParentPciAddress: {StringValue: &root},
				sriovconsts.AttributeNumaNode:         {IntValue: &numa},
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

var _ = Describe("SriovResourceFilterReconciler (envtest)", func() {
	It("should handle no filters in namespace", func(ctx SpecContext) {
		Consistently(func() bool {
			return reconciler.GetCurrentResourceFilter() == nil
		}, 500*time.Millisecond, 100*time.Millisecond).Should(BeTrue())
		Expect(applied).To(HaveLen(0))
	})

	It("should select filter with empty nodeSelector and apply to devices", func(ctx SpecContext) {
		filter := &sriovdrav1alpha1.SriovResourceFilter{
			ObjectMeta: metav1.ObjectMeta{Name: "rf-empty-selector", Namespace: "dra-sriov-driver"},
			Spec: sriovdrav1alpha1.SriovResourceFilterSpec{
				NodeSelector: map[string]string{},
				Configs: []sriovdrav1alpha1.Config{{
					ResourceName:    "example.com/resA",
					ResourceFilters: nil,
				}},
			},
		}
		Expect(k8sClient.Create(ctx, filter)).To(Succeed())

		Eventually(func() *sriovdrav1alpha1.SriovResourceFilter {
			return reconciler.GetCurrentResourceFilter()
		}, 5*time.Second, 200*time.Millisecond).ShouldNot(BeNil())
		Expect(reconciler.HasResourceFilter()).To(BeTrue())
		Expect(reconciler.GetResourceNames()).To(ContainElement("example.com/resA"))

		Eventually(func() int { return len(applied) }, 2*time.Second, 100*time.Millisecond).Should(BeNumerically(">=", 1))
	})

	It("should ignore filters in other namespaces", func(ctx SpecContext) {
		otherNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-ns"}}
		Expect(k8sClient.Create(ctx, otherNS)).To(Succeed())

		filter := &sriovdrav1alpha1.SriovResourceFilter{
			ObjectMeta: metav1.ObjectMeta{Name: "rf-other-ns", Namespace: "other-ns"},
			Spec:       sriovdrav1alpha1.SriovResourceFilterSpec{Configs: []sriovdrav1alpha1.Config{{ResourceName: "example.com/ignored"}}},
		}
		Expect(k8sClient.Create(ctx, filter)).To(Succeed())

		Consistently(func() []string { return reconciler.GetResourceNames() }, 1*time.Second, 200*time.Millisecond).Should(ContainElement("example.com/resA"))
	})

	It("should handle multiple matching filters by clearing current filter", func(ctx SpecContext) {
		filter := &sriovdrav1alpha1.SriovResourceFilter{
			ObjectMeta: metav1.ObjectMeta{Name: "rf-duplicate", Namespace: "dra-sriov-driver"},
			Spec: sriovdrav1alpha1.SriovResourceFilterSpec{
				NodeSelector: map[string]string{},
				Configs:      []sriovdrav1alpha1.Config{{ResourceName: "example.com/resB"}},
			},
		}
		Expect(k8sClient.Create(ctx, filter)).To(Succeed())

		Eventually(func() bool { return reconciler.GetCurrentResourceFilter() == nil }, 5*time.Second, 200*time.Millisecond).Should(BeTrue())
	})

	It("should reselect when node labels change", func(ctx SpecContext) {
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "test-node"}, node)).To(Succeed())
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels["role"] = "dpdk"
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		for _, name := range []string{"rf-empty-selector", "rf-duplicate"} {
			rf := &sriovdrav1alpha1.SriovResourceFilter{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "dra-sriov-driver", Name: name}, rf); err == nil {
				_ = k8sClient.Delete(ctx, rf)
			}
		}

		filter := &sriovdrav1alpha1.SriovResourceFilter{
			ObjectMeta: metav1.ObjectMeta{Name: "rf-node-select", Namespace: "dra-sriov-driver"},
			Spec: sriovdrav1alpha1.SriovResourceFilterSpec{
				NodeSelector: map[string]string{"role": "dpdk"},
				Configs:      []sriovdrav1alpha1.Config{{ResourceName: "example.com/resC"}},
			},
		}
		Expect(k8sClient.Create(ctx, filter)).To(Succeed())

		Eventually(func() []string { return reconciler.GetResourceNames() }, 5*time.Second, 200*time.Millisecond).Should(ContainElement("example.com/resC"))
	})

	It("should requeue when node is missing (direct Reconcile call)", func(ctx SpecContext) {
		bogus := controller.NewSriovResourceFilterReconciler(k8sClient, "missing-node", "dra-sriov-driver", nil)
		result, err := bogus.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "irrelevant", Namespace: "dra-sriov-driver"}})
		Expect(err).To(BeNil())
		Expect(result.RequeueAfter).NotTo(BeZero())
	})
})
