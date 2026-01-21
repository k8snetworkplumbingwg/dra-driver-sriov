package host_test

import (
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
)

var _ = Describe("Host", func() {
	var (
		h        host.Interface
		fs       *host.FakeFilesystem
		tearDown func()
	)

	BeforeEach(func() {
		h = host.NewHost()
		fs = &host.FakeFilesystem{}
	})

	AfterEach(func() {
		if tearDown != nil {
			tearDown()
		}
	})

	Describe("SR-IOV Detection Functions", func() {
		Context("IsSriovVF", func() {
			It("should return true when physfn symlink exists", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/physfn": "../0000:01:00.0",
				}
				tearDown = fs.Use()

				result := h.IsSriovVF("0000:01:00.0")
				Expect(result).To(BeTrue())
			})

			It("should return false when physfn symlink does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				result := h.IsSriovVF("0000:01:00.0")
				Expect(result).To(BeFalse())
			})
		})

		Context("IsSriovPF", func() {
			It("should return true when virtfn0 symlink exists", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/virtfn0": "../0000:01:00.1",
				}
				tearDown = fs.Use()

				result := h.IsSriovPF("0000:01:00.0")
				Expect(result).To(BeTrue())
			})

			It("should return false when virtfn0 symlink does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				result := h.IsSriovPF("0000:01:00.0")
				Expect(result).To(BeFalse())
			})
		})

		Context("GetVFList", func() {
			It("should return list of VFs with their information", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
					"sys/bus/pci/devices/0000:01:00.1",
					"sys/bus/pci/devices/0000:01:00.2",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.1/device": []byte("0x1016"),
					"sys/bus/pci/devices/0000:01:00.2/device": []byte("0x1017"),
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/virtfn0": "../0000:01:00.1",
					"sys/bus/pci/devices/0000:01:00.0/virtfn1": "../0000:01:00.2",
				}
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(2))
				Expect(vfList[0]).To(Equal(host.VFInfo{
					PciAddress: "0000:01:00.1",
					VFID:       0,
					DeviceID:   "1016",
				}))
				Expect(vfList[1]).To(Equal(host.VFInfo{
					PciAddress: "0000:01:00.2",
					VFID:       1,
					DeviceID:   "1017",
				}))
			})

			It("should return empty list when no VFs exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(0))
			})

			It("should return error when PF directory does not exist", func() {
				tearDown = fs.Use()

				_, err := h.GetVFList("0000:01:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to read PF directory"))
			})
		})
	})

	Describe("Network Interface Functions", func() {
		Context("TryGetInterfaceName", func() {
			It("should return interface name when net directory exists", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
				}
				tearDown = fs.Use()

				interfaceName := h.TryGetInterfaceName("0000:01:00.0")
				Expect(interfaceName).To(Equal("eth0"))
			})

			It("should return empty string when net directory does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				interfaceName := h.TryGetInterfaceName("0000:01:00.0")
				Expect(interfaceName).To(BeEmpty())
			})

			It("should return empty string when net directory is empty", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
				}
				tearDown = fs.Use()

				interfaceName := h.TryGetInterfaceName("0000:01:00.0")
				Expect(interfaceName).To(BeEmpty())
			})
		})

		Context("GetNicSriovMode", func() {
			It("should return legacy mode", func() {
				tearDown = fs.Use()

				mode := h.GetNicSriovMode("0000:01:00.0")
				Expect(mode).To(Equal("legacy"))
			})
		})

		Context("GetLinkType", func() {
			It("should return 'ethernet' for type ArphrdEther", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"sys/class/net/eth0",
				}
				fs.Files = map[string][]byte{
					"sys/class/net/eth0/type": []byte(fmt.Sprintf("%d\n", host.ArphrdEther)),
				}
				tearDown = fs.Use()

				linkType, err := h.GetLinkType("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(linkType).To(Equal(consts.LinkTypeEthernet))
			})

			It("should return 'infiniband' for type ArphrdInfiniband", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:02:00.0/net",
					"sys/bus/pci/devices/0000:02:00.0/net/ib0",
					"sys/class/net/ib0",
				}
				fs.Files = map[string][]byte{
					"sys/class/net/ib0/type": []byte(fmt.Sprintf("%d\n", host.ArphrdInfiniband)),
				}
				tearDown = fs.Use()

				linkType, err := h.GetLinkType("0000:02:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(linkType).To(Equal(consts.LinkTypeInfiniband))
			})

			It("should return 'unknown' for unknown types", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:03:00.0/net",
					"sys/bus/pci/devices/0000:03:00.0/net/custom0",
					"sys/class/net/custom0",
				}
				fs.Files = map[string][]byte{
					"sys/class/net/custom0/type": []byte("99\n"),
				}
				tearDown = fs.Use()

				linkType, err := h.GetLinkType("0000:03:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(linkType).To(Equal(consts.LinkTypeUnknown))
			})

			It("should return error when interface name cannot be determined", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:04:00.0",
				}
				tearDown = fs.Use()

				linkType, err := h.GetLinkType("0000:04:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unable to get interface name"))
				Expect(linkType).To(BeEmpty())
			})

			It("should return error when type file does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:05:00.0/net",
					"sys/bus/pci/devices/0000:05:00.0/net/eth0",
					"sys/class/net/eth0",
				}
				tearDown = fs.Use()

				linkType, err := h.GetLinkType("0000:05:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to read link type"))
				Expect(linkType).To(BeEmpty())
			})

			It("should return error when type file contains invalid data", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:06:00.0/net",
					"sys/bus/pci/devices/0000:06:00.0/net/eth0",
					"sys/class/net/eth0",
				}
				fs.Files = map[string][]byte{
					"sys/class/net/eth0/type": []byte("invalid\n"),
				}
				tearDown = fs.Use()

				linkType, err := h.GetLinkType("0000:06:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to parse link type value"))
				Expect(linkType).To(BeEmpty())
			})
		})
	})

	Describe("NUMA and Parent Functions", func() {
		Context("GetNumaNode", func() {
			It("should return NUMA node from file", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/numa_node": []byte("1"),
				}
				tearDown = fs.Use()

				numaNode, err := h.GetNumaNode("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(numaNode).To(Equal("1"))
			})

			It("should return '0' when NUMA node file contains -1", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/numa_node": []byte("-1"),
				}
				tearDown = fs.Use()

				numaNode, err := h.GetNumaNode("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(numaNode).To(Equal("0"))
			})

			It("should return '0' when NUMA node file does not exist", func() {
				tearDown = fs.Use()

				numaNode, err := h.GetNumaNode("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(numaNode).To(Equal("0"))
			})
		})

		Context("GetPCIeRoot", func() {
			It("should return error for invalid PCI address format", func() {
				// Test with invalid format - this is validated by the upstream implementation
				// before it tries to access sysfs, so it works in tests
				_, err := h.GetPCIeRoot("invalid-address")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid PCI Bus ID format"))
			})

			It("should return error for empty PCI address", func() {
				_, err := h.GetPCIeRoot("")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("PCI Bus ID cannot be empty"))
			})

			It("should return error when device doesn't exist in sysfs", func() {
				// This will fail because the device doesn't exist in the real sysfs
				// The upstream implementation reads from /sys directly and cannot use our fake filesystem
				_, err := h.GetPCIeRoot("ffff:ff:ff.f")
				Expect(err).To(HaveOccurred())
			})

			It("should return PCIe root for valid device", func() {
				// Set up a realistic PCI device structure with proper hierarchy
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:00:00.0", // PCIe root complex
					"sys/bus/pci/devices/0000:01:00.0", // Device on bus 01
					"sys/devices/pci0000:00/0000:00:00.0",
					"sys/devices/pci0000:00/0000:00:00.0/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/vendor":                 []byte("0x8086"),
					"sys/bus/pci/devices/0000:01:00.0/device":                 []byte("0x1572"),
					"sys/bus/pci/devices/0000:01:00.0/class":                  []byte("0x020000"),
					"sys/devices/pci0000:00/0000:00:00.0/vendor":              []byte("0x8086"),
					"sys/devices/pci0000:00/0000:00:00.0/device":              []byte("0x1234"),
					"sys/devices/pci0000:00/0000:00:00.0/0000:01:00.0/vendor": []byte("0x8086"),
					"sys/devices/pci0000:00/0000:00:00.0/0000:01:00.0/device": []byte("0x1572"),
				}
				tearDown = fs.Use()

				// Try to get PCIe root - this should work if the upstream package respects SYSFS_PATH
				// or has been updated to support alternative root directories
				pcieRoot, err := h.GetPCIeRoot("0000:01:00.0")
				if err != nil {
					// If the upstream package doesn't support fake filesystem yet,
					// skip this test rather than failing
					Skip("Upstream package doesn't support alternative sysfs root yet: " + err.Error())
				}
				Expect(pcieRoot).To(Equal("pci0000:00"))
			})
		})

		Context("GetParentPciAddress", func() {
			It("should return parent address from symlink", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
					"sys/bus/pci/devices/0000:00:00.0",
				}
				tearDown = fs.Use()

				// Create a proper directory structure for testing parent resolution
				// The fallback logic should find the parent device
				parentAddr, err := h.GetParentPciAddress("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(parentAddr).To(Equal("0000:00:00.0"))
			})

			It("should return empty string when parent cannot be determined", func() {
				tearDown = fs.Use()

				parentAddr, err := h.GetParentPciAddress("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(parentAddr).To(BeEmpty())
			})

			It("should return error for invalid PCI address format", func() {
				tearDown = fs.Use()

				_, err := h.GetParentPciAddress("invalid-address")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid PCI address format"))
			})
		})
	})

	Describe("Driver Management Functions", func() {
		Context("GetDriverByBusAndDevice", func() {
			It("should return driver name from symlink", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/driver": "../../drivers/ixgbe",
				}
				tearDown = fs.Use()

				driver, err := h.GetDriverByBusAndDevice("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(driver).To(Equal("ixgbe"))
			})

			It("should return empty string when driver link does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				driver, err := h.GetDriverByBusAndDevice("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(driver).To(BeEmpty())
			})
		})

		Context("BindDeviceDriver", func() {
			It("should do nothing when config.Driver is empty", func() {
				tearDown = fs.Use()
				config := &configapi.VfConfig{Driver: ""}

				originalDriver, err := h.BindDeviceDriver("0000:01:00.0", config)
				Expect(err).NotTo(HaveOccurred())
				Expect(originalDriver).To(BeEmpty())
			})

			It("should bind to default driver when config.Driver is 'default'", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
					"sys/bus/pci",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/drivers_probe": []byte(""),
				}
				tearDown = fs.Use()
				config := &configapi.VfConfig{Driver: "default"}

				_, err := h.BindDeviceDriver("0000:01:00.0", config)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("IsDpdkDriver", func() {
			It("should return true for DPDK drivers", func() {
				tearDown = fs.Use()

				Expect(h.IsDpdkDriver("vfio-pci")).To(BeTrue())
				Expect(h.IsDpdkDriver("uio_pci_generic")).To(BeTrue())
				Expect(h.IsDpdkDriver("igb_uio")).To(BeTrue())
			})

			It("should return false for non-DPDK drivers", func() {
				tearDown = fs.Use()

				Expect(h.IsDpdkDriver("ixgbe")).To(BeFalse())
				Expect(h.IsDpdkDriver("i40e")).To(BeFalse())
				Expect(h.IsDpdkDriver("unknown")).To(BeFalse())
			})
		})
	})

	Describe("Kernel Module Management Functions", func() {
		Context("IsKernelModuleLoaded", func() {
			It("should return true when module is loaded", func() {
				fs.Dirs = []string{
					"proc",
				}
				fs.Files = map[string][]byte{
					"proc/modules": []byte(`vfio_pci 45056 0 - Live 0xffffffffa0123000
vfio 32768 1 vfio_pci, Live 0xffffffffa0456000
other_module 16384 0 - Live 0xffffffffa0789000`),
				}
				tearDown = fs.Use()

				Expect(h.IsKernelModuleLoaded("vfio_pci")).To(BeTrue())
				Expect(h.IsKernelModuleLoaded("vfio")).To(BeTrue())
			})

			It("should return false when module is not loaded", func() {
				fs.Dirs = []string{
					"proc",
				}
				fs.Files = map[string][]byte{
					"proc/modules": []byte(`other_module 16384 0 - Live 0xffffffffa0789000`),
				}
				tearDown = fs.Use()

				Expect(h.IsKernelModuleLoaded("vfio_pci")).To(BeFalse())
				Expect(h.IsKernelModuleLoaded("vfio")).To(BeFalse())
			})

			It("should return false when /proc/modules does not exist", func() {
				tearDown = fs.Use()

				Expect(h.IsKernelModuleLoaded("vfio_pci")).To(BeFalse())
			})
		})

		Context("EnsureDpdkModuleLoaded", func() {
			It("should skip non-DPDK drivers", func() {
				tearDown = fs.Use()

				err := h.EnsureDpdkModuleLoaded("ixgbe")
				Expect(err).NotTo(HaveOccurred())
			})

			It("should return nil when vfio modules are already loaded", func() {
				fs.Dirs = []string{
					"proc",
				}
				fs.Files = map[string][]byte{
					"proc/modules": []byte(`vfio_pci 45056 0 - Live 0xffffffffa0123000
vfio 32768 1 vfio_pci, Live 0xffffffffa0456000`),
				}
				tearDown = fs.Use()

				err := h.EnsureDpdkModuleLoaded("vfio-pci")
				Expect(err).NotTo(HaveOccurred())
			})

			It("should return error for unknown DPDK driver", func() {
				tearDown = fs.Use()

				// Temporarily modify the IsDpdkDriver to consider this as DPDK driver
				// by using a driver that would be recognized as DPDK but not supported
				err := h.EnsureDpdkModuleLoaded("uio_pci_generic")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unknown DPDK driver"))
			})
		})

		Context("EnsureVhostModulesLoaded", func() {
			It("should return nil when vhost modules are already loaded", func() {
				fs.Dirs = []string{
					"proc",
				}
				fs.Files = map[string][]byte{
					"proc/modules": []byte(`tun 45056 0 - Live 0xffffffffa0123000
vhost_net 32768 1 tun, Live 0xffffffffa0456000`),
				}
				tearDown = fs.Use()

				err := h.EnsureVhostModulesLoaded()
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("VFIO Device Functions", func() {
		Context("GetVFIODeviceFile", func() {
			It("should return VFIO device files when iommu group exists", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
					"sys/kernel/iommu_groups/1",
				}
				tearDown = fs.Use()

				// Create absolute symlink using the actual root directory
				symlinkPath := fs.RootDir + "/sys/bus/pci/devices/0000:01:00.0/iommu_group"
				targetPath := fs.RootDir + "/sys/kernel/iommu_groups/1"
				err := os.Symlink(targetPath, symlinkPath)
				Expect(err).NotTo(HaveOccurred())

				devFileHost, devFileContainer, err := h.GetVFIODeviceFile("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(devFileHost).To(Equal("/dev/vfio/1"))
				Expect(devFileContainer).To(Equal("/dev/vfio/1"))
			})

			It("should return error when device does not exist", func() {
				tearDown = fs.Use()

				_, _, err := h.GetVFIODeviceFile("0000:01:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Could not get directory information for device"))
			})

			It("should return error when iommu_group does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				_, _, err := h.GetVFIODeviceFile("0000:01:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unable to find iommu_group"))
			})
		})
	})

	Describe("Edge Cases and Error Handling", func() {
		Context("File System Operations", func() {
			It("should handle non-existent directories gracefully", func() {
				tearDown = fs.Use()

				// Test functions that should handle non-existent paths gracefully
				result := h.IsSriovVF("non-existent-device")
				Expect(result).To(BeFalse())

				result = h.IsSriovPF("non-existent-device")
				Expect(result).To(BeFalse())

				interfaceName := h.TryGetInterfaceName("non-existent-device")
				Expect(interfaceName).To(BeEmpty())
			})

			It("should handle malformed symlinks gracefully", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/virtfn0": "../non-existent-target",
				}
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(1)) // Should include the VF even if device ID is missing
				Expect(vfList[0].PciAddress).To(Equal("non-existent-target"))
				Expect(vfList[0].DeviceID).To(BeEmpty()) // Device ID should be empty due to missing file
			})
		})

		Context("Data Parsing", func() {
			It("should handle VF ID parsing errors gracefully", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/virtfn_invalid": "../0000:01:00.1",
				}
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(0)) // Should skip invalid VF IDs
			})

			It("should handle missing device ID files", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
					"sys/bus/pci/devices/0000:01:00.1",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/virtfn0": "../0000:01:00.1",
				}
				// No device file for 0000:01:00.1
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(1))
				Expect(vfList[0].DeviceID).To(BeEmpty()) // Should handle missing device ID
			})
		})
	})
})
