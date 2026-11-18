package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	metadatav1alpha1 "k8s.io/dynamic-resource-allocation/api/metadata/v1alpha1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	hostmock "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// driverWithTestConfig builds a minimal driver for rejection-before-side-effects tests.
func driverWithTestConfig() *Driver {
	return &Driver{config: &types.Config{Flags: &types.Flags{}}}
}

// preparedTestClaim builds a class-only allocation and matching prepared-device provenance.
func preparedTestClaim(uid k8stypes.UID, pciAddress string) (*resourceapi.ResourceClaim, types.PreparedDevices, *types.PreparedClaimMeta) {
	cfg := configapi.DefaultVfConfig()
	cfg.NetAttachDefName, cfg.NetAttachDefNamespace = "blue-net", "admin"
	raw, err := json.Marshal(cfg)
	Expect(err).NotTo(HaveOccurred())
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: string(uid), UID: uid},
		Spec: resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{
			{Name: "vf", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "blue"}},
		}}},
		Status: resourceapi.ResourceClaimStatus{
			ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: "pod", UID: "pod-uid"}},
			Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{
				Results: []resourceapi.DeviceRequestAllocationResult{{Request: "vf", Driver: consts.DriverName, Pool: "node-a", Device: "dev-" + string(uid)}},
				Config: []resourceapi.DeviceAllocationConfiguration{{Source: resourceapi.AllocationConfigSourceClass,
					DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: consts.DriverName, Parameters: runtime.RawExtension{Raw: raw}}}}},
			}},
		},
	}
	resolved, err := devicestate.ResolveClaimConfiguration(claim, configapi.Decoder, string(consts.ConfigurationModeStandalone))
	Expect(err).NotTo(HaveOccurred())
	devices := types.PreparedDevices{{
		Device:              drapbv1.Device{RequestNames: []string{"vf"}, PoolName: "node-a", DeviceName: "dev-" + string(uid)},
		ClaimNamespacedName: kubeletplugin.NamespacedObject{NamespacedName: k8stypes.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}, UID: uid},
		Config:              cfg.DeepCopy(), IfName: "net-" + string(uid), PciAddress: pciAddress, PodUID: "pod-uid",
		ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{
			Env: []string{"TEST_CLAIM_UID=" + string(uid)},
		}},
	}}
	return claim, devices, &types.PreparedClaimMeta{ConfigPolicyVersion: resolved.ConfigPolicyVersion, ConfigFingerprint: resolved.Fingerprint}
}

var _ = Describe("Driver", func() {
	Context("PrepareResourceClaims orchestrator", func() {
		It("returns immediately with empty input", func() {
			d := &Driver{}
			result, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeEmpty())
		})

		It("errors when no prepared devices exist for the pod after processing", func() {
			flags := &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}
			flags.ConfigurationMode = string(consts.ConfigurationModeMultus)
			cfg := &types.Config{Flags: flags}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).ToNot(HaveOccurred())

			d := &Driver{podManager: pm}

			// Claim with ReservedFor but no Allocation -> inner prepare will error, then final GetDevicesByPodUID fails
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc1", UID: k8stypes.UID("rc-uid")}}
			claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("pod-uid")}}

			results, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
			Expect(err).NotTo(HaveOccurred())
			Expect(results[claim.UID].Err).To(MatchError("claim not yet allocated"))
		})

		It("returns error instead of panicking when no claim contains pod info", func() {
			flags := &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}
			flags.ConfigurationMode = string(consts.ConfigurationModeMultus)
			cfg := &types.Config{Flags: flags}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).ToNot(HaveOccurred())

			d := &Driver{podManager: pm}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "rc1",
					UID:       k8stypes.UID("rc-uid"),
				},
			}

			_, err = d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no pod info found for prepared claims"))
		})
	})

	Context("prepareResourceClaim guards", func() {
		It("errors when ReservedFor is empty", func() {
			d := &Driver{}
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")}}
			res := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("no pod info found"))
		})

		It("errors when multiple pods in ReservedFor", func() {
			d := &Driver{}
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")}}
			claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: "a"}, {UID: "b"}}
			res := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("multiple pods"))
		})

		It("errors when Allocation is nil", func() {
			d := &Driver{}
			claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")}}
			claim.Status.ReservedFor = []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("pod-uid")}}
			res := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("claim not yet allocated"))
		})

		It("rejects claim spec opaque configuration before prepare", func() {
			d := driverWithTestConfig()
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: k8stypes.UID("rc-uid")},
				Spec: resourceapi.ResourceClaimSpec{
					Devices: resourceapi.DeviceClaim{
						Requests: []resourceapi.DeviceRequest{{Name: "vf", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "test-class"}}},
						Config: []resourceapi.DeviceClaimConfiguration{
							{
								DeviceConfiguration: resourceapi.DeviceConfiguration{
									Opaque: &resourceapi.OpaqueDeviceConfiguration{
										Driver: "sriovnetwork.k8snetworkplumbingwg.io",
									},
								},
							},
						},
					},
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{UID: k8stypes.UID("pod-uid")}},
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: "sriovnetwork.k8snetworkplumbingwg.io", Device: "dev1", Request: "vf", Pool: "pool1"},
							},
						},
					},
				},
			}
			res := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("not allowed in claim spec"))
		})

		It("rejects cached prepare state without verified provenance", func() {
			flags := &types.Flags{KubeletPluginsDirectoryPath: GinkgoT().TempDir()}
			flags.ConfigurationMode = string(consts.ConfigurationModeMultus)
			cfg := &types.Config{Flags: flags}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).NotTo(HaveOccurred())

			podUID := k8stypes.UID("pod-uid")
			claimUID := k8stypes.UID("rc-uid")
			devices := types.PreparedDevices{
				{Device: drapbv1.Device{DeviceName: "dev1"}},
			}
			Expect(pm.Set(podUID, claimUID, devices, nil)).To(Succeed())

			d := &Driver{config: cfg, podManager: pm}
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rc", UID: claimUID},
				Spec:       resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{{Name: "vf", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "test-class"}}}}},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{{UID: podUID}},
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: consts.DriverName, Device: "dev1", Request: "vf", Pool: "pool1"},
							},
						},
					},
				},
			}
			res := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(res.Err).To(HaveOccurred())
			Expect(res.Err.Error()).To(ContainSubstring("migration"))
		})
	})

	Context("prepared claim lifecycle", Serial, func() {
		var (
			d           *Driver
			claim       *resourceapi.ResourceClaim
			devices     types.PreparedDevices
			meta        *types.PreparedClaimMeta
			globalPath  string
			originalCDI []byte
		)
		BeforeEach(func() {
			ctrl := gomock.NewController(GinkgoT())
			original := host.GetHelpers()
			DeferCleanup(func() { host.Helpers = original })
			DeferCleanup(ctrl.Finish)
			mockHost := hostmock.NewMockInterface(ctrl)
			host.Helpers = mockHost
			// Discovery sees a non-network PCI device; no real hardware is queried.
			mockHost.EXPECT().PCI().Return(&pci.Info{Devices: []*pci.Device{{Class: &pcidb.Class{ID: "00"}}}}, nil)
			mockHost.EXPECT().IsIommufdAvailable().Return(false)
			cfg := &types.Config{Flags: &types.Flags{ConfigurationMode: string(consts.ConfigurationModeStandalone), KubeletPluginsDirectoryPath: GinkgoT().TempDir()}}
			pm, err := podmanager.NewPodManager(cfg)
			Expect(err).NotTo(HaveOccurred())
			cdiDir := GinkgoT().TempDir()
			handler, err := cdi.NewHandler(cdiDir)
			Expect(err).NotTo(HaveOccurred())
			manager, err := devicestate.NewManager(cfg, handler, nil)
			Expect(err).NotTo(HaveOccurred())
			d = &Driver{config: cfg, podManager: pm, cdi: handler, deviceStateManager: manager}
			claim, devices, meta = preparedTestClaim("a", "0000:01:00.1")
			Expect(pm.Set("pod-uid", claim.UID, devices, meta)).To(Succeed())
			Expect(handler.CreateGlobalPodSpecFile("pod-uid", []string{devices[0].PciAddress})).To(Succeed())
			files, err := os.ReadDir(cdiDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
			globalPath = filepath.Join(cdiDir, files[0].Name())
			originalCDI, err = os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(handler.CreateClaimSpecFile(devices)).To(Succeed())
		})

		It("reuses a matching cached preparation without host or API operations", func() {
			result := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(result.Err).NotTo(HaveOccurred())
			Expect(result.Devices).To(Equal([]kubeletplugin.Device{devices[0].ToKubeletPluginDevice(nil)}))
		})

		It("rejects cached preparation after a configuration mode change", func() {
			Expect(d.prepareResourceClaim(context.Background(), new(int), claim, nil).Err).NotTo(HaveOccurred())
			d.config.Flags.ConfigurationMode = string(consts.ConfigurationModeMultus)
			Expect(d.prepareResourceClaim(context.Background(), new(int), claim, nil).Err).To(MatchError(ContainSubstring("migration")))
			_, found := d.podManager.Get("pod-uid", claim.UID)
			Expect(found).To(BeTrue(), "a mode change must retain the old record for cleanup")
		})

		DescribeTable("rejects stale cached preparation while retaining cleanup state", func(change func(*types.PreparedClaimMeta, *types.PreparedDevice)) {
			change(meta, devices[0])
			Expect(d.podManager.Set("pod-uid", claim.UID, devices, meta)).To(Succeed())
			result := d.prepareResourceClaim(context.Background(), new(int), claim, nil)
			Expect(result.Err).To(MatchError(ContainSubstring("migration")))
			_, found := d.podManager.Get("pod-uid", claim.UID)
			Expect(found).To(BeTrue())
			data, err := os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal(originalCDI))
		},
			Entry("policy version", func(m *types.PreparedClaimMeta, _ *types.PreparedDevice) { m.ConfigPolicyVersion = "old-policy" }),
			Entry("fingerprint", func(m *types.PreparedClaimMeta, _ *types.PreparedDevice) { m.ConfigFingerprint = "mismatch" }),
			Entry("applied config", func(_ *types.PreparedClaimMeta, p *types.PreparedDevice) { p.Config.AddVhostMount = true }),
			Entry("share identity", func(_ *types.PreparedClaimMeta, p *types.PreparedDevice) { p.ShareID = "other-share" }),
		)

		It("rejects claim overrides before a matching cache can be reused", func() {
			claim.Spec.Devices.Config = []resourceapi.DeviceClaimConfiguration{{DeviceConfiguration: claim.Status.Allocation.Devices.Config[0].DeviceConfiguration}}
			result, err := d.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
			Expect(err).NotTo(HaveOccurred())
			Expect(result[claim.UID].Err).To(MatchError(ContainSubstring("not allowed in claim spec")))
			data, err := os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal(originalCDI))
		})

		DescribeTable("preserves another claim's Pod CDI during cleanup and removes it for the last claim", func(checkpointed bool) {
			other, otherDevices, otherMeta := preparedTestClaim("b", "0000:01:00.2")
			Expect(d.cdi.CreateClaimSpecFile(otherDevices)).To(Succeed())
			if checkpointed {
				Expect(d.podManager.Set("pod-uid", other.UID, otherDevices, otherMeta)).To(Succeed())
				// Force publication failure without changing A's existing CDI file.
				blocked := GinkgoT().TempDir()
				failingCDI, err := cdi.NewHandler(blocked)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.Remove(blocked)).To(Succeed())
				Expect(os.WriteFile(blocked, []byte("not a directory"), 0o600)).To(Succeed())
				originalHandler := d.cdi
				d.cdi = failingCDI
				results := map[k8stypes.UID]kubeletplugin.PrepareResult{claim.UID: {}, other.UID: {}}
				err = d.publishPodCDI(context.Background(), "pod-uid", []*resourceapi.ResourceClaim{claim, other}, results, map[k8stypes.UID]bool{other.UID: true})
				d.cdi = originalHandler
				Expect(err).To(MatchError(ContainSubstring("error creating global spec file")))
			} else {
				// A failed Set has no record for B; preparation cleanup must preserve A.
				Expect(d.deviceStateManager.Unprepare(string(other.UID), otherDevices)).To(Succeed())
			}
			data, err := os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal(originalCDI))
			_, found := d.podManager.Get("pod-uid", other.UID)
			Expect(found).To(BeFalse())
			Expect(d.unprepareResourceClaim(context.Background(), devices[0].ClaimNamespacedName)).To(Succeed())
			_, err = os.Stat(globalPath)
			Expect(os.IsNotExist(err)).To(BeTrue())
		}, Entry("checkpoint failure cleanup", false), Entry("publication rollback", true))

		It("retains the last claim for retry when shared CDI removal fails", func() {
			Expect(os.Remove(globalPath)).To(Succeed())
			Expect(os.Mkdir(globalPath, 0o700)).To(Succeed())
			busy := filepath.Join(globalPath, "busy")
			Expect(os.WriteFile(busy, []byte("busy"), 0o600)).To(Succeed())
			Expect(d.unprepareResourceClaim(context.Background(), devices[0].ClaimNamespacedName)).NotTo(Succeed())
			_, found := d.podManager.Get("pod-uid", claim.UID)
			Expect(found).To(BeTrue())
			Expect(os.Remove(busy)).To(Succeed())
			Expect(d.unprepareResourceClaim(context.Background(), devices[0].ClaimNamespacedName)).To(Succeed())
			_, found = d.podManager.Get("pod-uid", claim.UID)
			Expect(found).To(BeFalse())
		})

		It("requires successful cleanup before the previous driver can verify the node checkpoint", func() {
			devices[0].Config.Driver = "vfio-pci"
			devices[0].OriginalDriver = "iavf"
			Expect(d.podManager.Set("pod-uid", claim.UID, devices, meta)).To(Succeed())
			host.GetHelpers().(*hostmock.MockInterface).EXPECT().RestoreDeviceDriver(devices[0].PciAddress, "iavf").Return(nil).Times(2)
			previousDriverVerify := func() error {
				data, err := os.ReadFile(filepath.Join(d.config.DriverPluginPath(), consts.DriverPluginCheckpointFile))
				Expect(err).NotTo(HaveOccurred())
				// Match the pre-provenance reader: unknown JSON fields are dropped before checksum verification.
				var previous struct {
					Checksum checksum.Checksum `json:"checksum"`
					V1       *struct {
						PreparedClaimsByPodUID types.PreparedClaimsByPodUID `json:"preparedClaimsByPodUID,omitempty"`
					} `json:"v1,omitempty"`
				}
				Expect(json.Unmarshal(data, &previous)).To(Succeed())
				stored := previous.Checksum
				previous.Checksum = 0
				canonical, err := json.Marshal(previous)
				Expect(err).NotTo(HaveOccurred())
				return stored.Verify(canonical)
			}
			Expect(previousDriverVerify()).NotTo(Succeed(), "a downgrade with provenance still present must not bypass the checksum")
			Expect(os.Remove(globalPath)).To(Succeed())
			Expect(os.Mkdir(globalPath, 0o700)).To(Succeed())
			busy := filepath.Join(globalPath, "busy")
			Expect(os.WriteFile(busy, []byte("busy"), 0o600)).To(Succeed())
			results, err := d.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{devices[0].ClaimNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(results[claim.UID]).NotTo(Succeed())
			Expect(previousDriverVerify()).NotTo(Succeed(), "failed cleanup still prevents downgrade")
			// Restore the newer driver and retry cleanup using its checkpoint, as documented.
			d.podManager, err = podmanager.NewPodManager(d.config)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Remove(busy)).To(Succeed())
			results, err = d.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{devices[0].ClaimNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(results[claim.UID]).To(Succeed())
			Expect(previousDriverVerify()).To(Succeed(), "the previous driver can start from the cleaned empty checkpoint")
		})

		DescribeTable("rolls back new claims when out-of-batch verification fails", func(failure string) {
			other, otherDevices, otherMeta := preparedTestClaim("b", "0000:01:00.2")
			Expect(d.podManager.Set("pod-uid", other.UID, otherDevices, otherMeta)).To(Succeed())
			Expect(d.cdi.CreateClaimSpecFile(otherDevices)).To(Succeed())
			apiClaim := claim.DeepCopy()
			client := k8sfake.NewSimpleClientset(apiClaim)
			d.client = client
			switch failure {
			case "client unavailable":
				d.client = nil
			case "API read":
				client.PrependReactor("get", "resourceclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("API unavailable")
				})
			case "configuration":
				apiClaim.Spec.Devices.Config = []resourceapi.DeviceClaimConfiguration{{DeviceConfiguration: apiClaim.Status.Allocation.Devices.Config[0].DeviceConfiguration}}
				d.client = k8sfake.NewSimpleClientset(apiClaim)
			case "provenance":
				meta.ConfigFingerprint = "mismatch"
				Expect(d.podManager.Set("pod-uid", claim.UID, devices, meta)).To(Succeed())
			}
			err := d.publishPodCDI(context.Background(), "pod-uid", []*resourceapi.ResourceClaim{other},
				map[k8stypes.UID]kubeletplugin.PrepareResult{other.UID: {}}, map[k8stypes.UID]bool{other.UID: true})
			Expect(err).To(HaveOccurred())
			_, found := d.podManager.Get("pod-uid", other.UID)
			Expect(found).To(BeFalse(), "new preparation must not be stranded by verification failure")
			_, found = d.podManager.Get("pod-uid", claim.UID)
			Expect(found).To(BeTrue(), "out-of-batch cleanup state must survive")
			saved, _ := d.podManager.ClaimProvenance("pod-uid", claim.UID)
			Expect(saved).To(Equal(*meta))
			data, err := os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal(originalCDI))
		}, Entry("client unavailable", "client unavailable"), Entry("API read", "API read"), Entry("configuration", "configuration"), Entry("provenance", "provenance"))

		It("includes verified out-of-batch claims in Pod CDI without changing their provenance", func() {
			other, otherDevices, otherMeta := preparedTestClaim("b", "0000:01:00.2")
			Expect(d.podManager.Set("pod-uid", other.UID, otherDevices, otherMeta)).To(Succeed())
			d.client = k8sfake.NewSimpleClientset(claim.DeepCopy())
			Expect(d.publishPodCDI(context.Background(), "pod-uid", []*resourceapi.ResourceClaim{other},
				map[k8stypes.UID]kubeletplugin.PrepareResult{other.UID: {}}, nil)).To(Succeed())
			data, err := os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(devices[0].PciAddress))
			Expect(string(data)).To(ContainSubstring(otherDevices[0].PciAddress))
			saved, _ := d.podManager.ClaimProvenance("pod-uid", claim.UID)
			Expect(saved).To(Equal(*meta))
		})

		It("excludes rejected cached claims from global Pod CDI publication", func() {
			other, otherDevices, otherMeta := preparedTestClaim("b", "0000:01:00.2")
			Expect(d.podManager.Set("pod-uid", other.UID, otherDevices, otherMeta)).To(Succeed())
			results := map[k8stypes.UID]kubeletplugin.PrepareResult{claim.UID: {Err: fmt.Errorf("configuration rejected")}, other.UID: {}}
			Expect(d.publishPodCDI(context.Background(), "pod-uid", []*resourceapi.ResourceClaim{claim, other}, results, nil)).To(Succeed())
			data, err := os.ReadFile(globalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(otherDevices[0].PciAddress))
			Expect(string(data)).NotTo(ContainSubstring(devices[0].PciAddress))
			_, found := d.podManager.Get("pod-uid", claim.UID)
			Expect(found).To(BeTrue(), "rejection must not discard cleanup state")
		})

		It("reserves saved and future fixed names and rejects cross-claim conflicts", func() {
			other, _, _ := preparedTestClaim("b", "0000:01:00.2")
			first, err := d.resolveResourceClaimConfiguration(claim)
			Expect(err).NotTo(HaveOccurred())
			second, err := d.resolveResourceClaimConfiguration(other)
			Expect(err).NotTo(HaveOccurred())
			for _, entry := range second.ByKey {
				entry.Config.IfName = "vfnet0"
			}
			resolved := map[k8stypes.UID]*devicestate.ResolvedClaimConfiguration{claim.UID: first, other.UID: second}
			claims := []*resourceapi.ResourceClaim{claim, other}
			Expect(reservePodInterfaceNames("pod-uid", claims, resolved, d.podManager.GetClaimsByPodUID("pod-uid"))).To(Succeed())
			Expect(first.ExistingIfNames).To(HaveKeyWithValue("vfnet0", other.UID))
			Expect(second.ExistingIfNames).To(HaveKeyWithValue(devices[0].IfName, claim.UID))
			for _, entry := range first.ByKey {
				entry.Config.IfName = "vfnet0"
			}
			Expect(reservePodInterfaceNames("pod-uid", claims, resolved, nil)).To(MatchError(ContainSubstring("conflicts between claims")))
		})
	})

	Context("HandleError", func() {
		It("calls cancelCtx on fatal errors", func() {
			called := false
			d := &Driver{cancelCtx: func(err error) { called = true }}
			d.HandleError(context.Background(), fmt.Errorf("fatal"), "oops")
			Expect(called).To(BeTrue())
		})
		It("does not cancel on recoverable errors", func() {
			called := false
			d := &Driver{cancelCtx: func(err error) { called = true }}
			d.HandleError(context.Background(), kubeletplugin.ErrRecoverable, "oops")
			Expect(called).To(BeFalse())
		})
	})

	Context("buildPluginOptions", func() {
		var (
			oldEnableDeviceMetadataOption func(bool) kubeletplugin.Option
			oldCDIDirectoryOption         func(string) kubeletplugin.Option
			oldMetadataVersionsOption     func(...schema.GroupVersion) kubeletplugin.Option
		)

		BeforeEach(func() {
			oldEnableDeviceMetadataOption = enableDeviceMetadataOption
			oldCDIDirectoryOption = cdiDirectoryOption
			oldMetadataVersionsOption = metadataVersionsOption
		})

		AfterEach(func() {
			enableDeviceMetadataOption = oldEnableDeviceMetadataOption
			cdiDirectoryOption = oldCDIDirectoryOption
			metadataVersionsOption = oldMetadataVersionsOption
		})

		It("uses configured CDI root when metadata is enabled", func() {
			flags := &types.Flags{
				NodeName:                      "node-a",
				CdiRoot:                       "/tmp/custom-cdi",
				KubeletRegistrarDirectoryPath: "/tmp/registry",
				KubeletPluginsDirectoryPath:   "/tmp/plugins",
				EnableDeviceMetadata:          true,
			}
			cfg := &types.Config{Flags: flags}

			metadataEnabledCalled := false
			cdiDir := ""
			metadataVersionCalled := false

			enableDeviceMetadataOption = func(enabled bool) kubeletplugin.Option {
				metadataEnabledCalled = enabled
				return oldEnableDeviceMetadataOption(enabled)
			}
			cdiDirectoryOption = func(path string) kubeletplugin.Option {
				cdiDir = path
				return oldCDIDirectoryOption(path)
			}
			metadataVersionsOption = func(versions ...schema.GroupVersion) kubeletplugin.Option {
				if len(versions) == 1 && versions[0] == metadatav1alpha1.SchemeGroupVersion {
					metadataVersionCalled = true
				}
				return oldMetadataVersionsOption(versions...)
			}

			opts := buildPluginOptions(cfg)
			Expect(opts).To(HaveLen(8))
			Expect(metadataEnabledCalled).To(BeTrue())
			Expect(cdiDir).To(Equal("/tmp/custom-cdi"))
			Expect(metadataVersionCalled).To(BeTrue())
		})

		It("does not append metadata options when metadata is disabled", func() {
			flags := &types.Flags{
				NodeName:                      "node-a",
				CdiRoot:                       "/tmp/custom-cdi",
				KubeletRegistrarDirectoryPath: "/tmp/registry",
				KubeletPluginsDirectoryPath:   "/tmp/plugins",
				EnableDeviceMetadata:          false,
			}
			cfg := &types.Config{Flags: flags}

			metadataEnabledCalled := false
			cdiCalled := false
			metadataVersionCalled := false

			enableDeviceMetadataOption = func(enabled bool) kubeletplugin.Option {
				metadataEnabledCalled = true
				return oldEnableDeviceMetadataOption(enabled)
			}
			cdiDirectoryOption = func(path string) kubeletplugin.Option {
				cdiCalled = true
				return oldCDIDirectoryOption(path)
			}
			metadataVersionsOption = func(versions ...schema.GroupVersion) kubeletplugin.Option {
				metadataVersionCalled = true
				return oldMetadataVersionsOption(versions...)
			}

			opts := buildPluginOptions(cfg)
			Expect(opts).To(HaveLen(5))
			Expect(metadataEnabledCalled).To(BeFalse())
			Expect(cdiCalled).To(BeFalse())
			Expect(metadataVersionCalled).To(BeFalse())
		})
	})
})
