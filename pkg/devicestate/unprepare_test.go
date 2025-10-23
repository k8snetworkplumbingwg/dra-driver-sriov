package devicestate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	hostmock "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host/mock"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

var _ = Describe("Manager", func() {
	Context("unprepareDevices", func() {
		It("restores original device drivers using host interface", func() {
			ctrl := gomock.NewController(GinkgoT())
			defer ctrl.Finish()

			// Ensure helpersOnce has run so our override sticks across GetHelpers()
			_ = host.GetHelpers()
			mockHost := hostmock.NewMockInterface(ctrl)
			originalHelpers := host.Helpers
			defer func() { host.Helpers = originalHelpers }()
			host.Helpers = mockHost

			mockHost.EXPECT().RestoreDeviceDriver("0000:00:00.1", "ixgbe").Return(nil).Times(1)

			s := &Manager{}
			devices := drasriovtypes.PreparedDevices{
				&drasriovtypes.PreparedDevice{PciAddress: "0000:00:00.1", OriginalDriver: "ixgbe", Config: &configapi.VfConfig{Driver: "vfio-pci"}},
			}
			err := s.unprepareDevices(devices)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
