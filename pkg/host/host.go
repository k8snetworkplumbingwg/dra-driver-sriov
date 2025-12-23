package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jaypipes/ghw"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

var (
	RootDir = ""
)

// Helper functions to build paths respecting RootDir

// buildSysPath constructs a path under /sys with RootDir prefix if set
func buildSysPath(path string) string {
	if RootDir != "" {
		return filepath.Join(RootDir, path)
	}
	return path
}

// buildSysBusPciPath constructs a PCI device path under /sys/bus/pci/devices
func buildSysBusPciPath(pciAddress, subPath string) string {
	basePath := filepath.Join(consts.SysBusPci, pciAddress)
	if subPath != "" {
		basePath = filepath.Join(basePath, subPath)
	}
	return buildSysPath(basePath)
}

// buildSysBusPciDriverPath constructs a driver path under /sys/bus/pci/drivers
func buildSysBusPciDriverPath(driver, subPath string) string {
	basePath := filepath.Join("/sys/bus/pci/drivers", driver)
	if subPath != "" {
		basePath = filepath.Join(basePath, subPath)
	}
	return buildSysPath(basePath)
}

// buildProcPath constructs a path under /proc with RootDir prefix if set
func buildProcPath(path string) string {
	if RootDir != "" {
		return filepath.Join(RootDir, path)
	}
	return path
}

// VFInfo holds information about a Virtual Function
type VFInfo struct {
	PciAddress string
	VFID       int
	DeviceID   string
}

// Interface defines the unified interface for all host system operations.
// This interface allows for easy mocking in unit tests by implementing mock versions
// of all the host-related methods.
//
//go:generate mockgen -destination mock/mock_host.go -source host.go
type Interface interface {
	// SR-IOV device utility functions
	IsSriovVF(pciAddress string) bool
	IsSriovPF(pciAddress string) bool
	GetVFList(pfPciAddress string) ([]VFInfo, error)

	// PCI device discovery functionality
	PCI() (*ghw.PCIInfo, error)

	// Network interface functions
	TryGetInterfaceName(pciAddr string) string
	GetNicSriovMode(pciAddr string) string

	// NUMA and topology functions
	GetNumaNode(pciAddress string) (string, error)
	GetPCIeRoot(pciAddress string) (string, error)
	GetParentPciAddress(pciAddress string) (string, error)

	// Driver binding operations
	BindDeviceDriver(pciAddress string, config *configapi.VfConfig) (string, error)
	BindDeviceDriverWithMAC(pciAddress string, config *configapi.VfConfig, macAddress string) (string, error)
	RestoreDeviceDriver(pciAddress string, originalDriver string) error

	// Low-level driver operations
	GetDriverByBusAndDevice(device string) (string, error)
	BindDriverByBusAndDevice(device, driver string) error
	UnbindDriverByBusAndDevice(device string) error
	BindDefaultDriver(pciAddress string) error

	// Driver utility functions
	IsDpdkDriver(driver string) bool

	// VF MAC address configuration
	SetVFMacAddress(vfPciAddress string, macAddress string) error

	// VFIO device functions
	GetVFIODeviceFile(pciAddress string) (devFileHost, devFileContainer string, err error)

	// Kernel module management functions
	IsKernelModuleLoaded(moduleName string) bool
	LoadKernelModule(moduleName string) error
	EnsureDpdkModuleLoaded(driver string) error
	EnsureVhostModulesLoaded() error
}

// Host provides unified host system functionality for SR-IOV, PCI operations, and driver management
type Host struct {
	log klog.Logger
}

// NewHost creates a new Host instance
func NewHost() Interface {
	return &Host{
		log: klog.FromContext(context.Background()).WithName("Host"),
	}
}

// Global Helpers instance for use throughout the application
var (
	Helpers     Interface
	helpersOnce sync.Once
)

// initHelpers initializes the global Helpers instance
func initHelpers() {
	helpersOnce.Do(func() {
		Helpers = NewHost()
	})
}

// GetHelpers returns the global Helpers instance, initializing it if necessary
func GetHelpers() Interface {
	initHelpers()
	return Helpers
}

// SR-IOV Detection Functions

// IsSriovVF checks if a PCI device is an SR-IOV Virtual Function
func (h *Host) IsSriovVF(pciAddress string) bool {
	// Check if physfn symlink exists - this indicates it's a VF
	physfnPath := buildSysBusPciPath(pciAddress, "physfn")
	if _, err := os.Lstat(physfnPath); err == nil {
		return true
	}
	return false
}

// IsSriovPF checks if a PCI device is an SR-IOV Physical Function
func (h *Host) IsSriovPF(pciAddress string) bool {
	// Check if virtfn0 symlink exists - this indicates it's a PF with VFs
	virtfnPath := buildSysBusPciPath(pciAddress, "virtfn0")
	if _, err := os.Lstat(virtfnPath); err == nil {
		return true
	}
	return false
}

// GetVFList returns list of VFs for a given PF with their VF IDs and device IDs
func (h *Host) GetVFList(pfPciAddress string) ([]VFInfo, error) {
	var vfList []VFInfo

	pfPath := buildSysBusPciPath(pfPciAddress, "")
	entries, err := os.ReadDir(pfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read PF directory: %v", err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "virtfn") {
			linkPath := filepath.Join(pfPath, entry.Name())
			target, err := os.Readlink(linkPath)
			if err != nil {
				continue
			}

			// Extract VF ID from directory name (virtfn0 -> 0, virtfn1 -> 1, etc.)
			vfIDStr := strings.TrimPrefix(entry.Name(), "virtfn")
			vfID, err := strconv.Atoi(vfIDStr)
			if err != nil {
				klog.Error(err, "Failed to parse VF ID", "entry", entry.Name(), "pfAddress", pfPciAddress)
				continue
			}

			// Extract PCI address from symlink target
			vfAddr := filepath.Base(target)

			// Read VF device ID from sysfs
			deviceIDPath := buildSysBusPciPath(vfAddr, "device")
			deviceIDBytes, err := os.ReadFile(deviceIDPath)
			vfDeviceID := ""
			if err != nil {
				klog.Error(err, "Failed to read VF device ID", "vfAddress", vfAddr, "pfAddress", pfPciAddress)
			} else {
				vfDeviceID = strings.TrimSpace(string(deviceIDBytes))
				// Remove 0x prefix if present
				vfDeviceID = strings.TrimPrefix(vfDeviceID, "0x")
			}

			vfList = append(vfList, VFInfo{
				PciAddress: vfAddr,
				VFID:       vfID,
				DeviceID:   vfDeviceID,
			})
		}
	}

	return vfList, nil
}

// PCI Hardware Discovery Functions

// PCI returns PCI information using the public ghw library
func (h *Host) PCI() (*ghw.PCIInfo, error) {
	return ghw.PCI()
}

// TryGetInterfaceName tries to find the network interface name based on PCI address
func (h *Host) TryGetInterfaceName(pciAddr string) string {
	netDir := buildSysBusPciPath(pciAddr, "net")
	if _, err := os.Lstat(netDir); err != nil {
		return ""
	}

	fInfos, err := os.ReadDir(netDir)
	if err != nil {
		return ""
	}

	if len(fInfos) == 0 {
		return ""
	}

	// Return the first network interface name found
	return fInfos[0].Name()
}

// GetNicSriovMode returns the interface mode (simplified implementation)
// This is a simplified version that returns "legacy" mode as fallback
func (h *Host) GetNicSriovMode(_ string) string {
	// For simplicity, always return legacy mode
	// A full implementation would use netlink to query the eswitch mode
	return "legacy"
}

// GetNumaNode returns the NUMA node for a given PCI device
func (h *Host) GetNumaNode(pciAddress string) (string, error) {
	numaNodePath := buildSysBusPciPath(pciAddress, "numa_node")
	content, err := os.ReadFile(numaNodePath)
	if err != nil {
		// If numa_node file doesn't exist, return "0" as default
		if os.IsNotExist(err) {
			return "0", nil
		}
		return "", fmt.Errorf("failed to read numa_node for %s: %v", pciAddress, err)
	}

	numaNode := strings.TrimSpace(string(content))
	// If numa_node contains -1, it means NUMA is not available, default to "0"
	if numaNode == "-1" {
		return "0", nil
	}

	return numaNode, nil
}

// GetParentPciAddress returns the parent PCI device address
func (h *Host) GetParentPciAddress(pciAddress string) (string, error) {
	// Parse the PCI address to get bus information
	// PCI address format: DDDD:BB:DD.F (domain:bus:device.function)
	parts := strings.Split(pciAddress, ":")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid PCI address format: %s", pciAddress)
	}

	domain := parts[0]
	deviceFunc := parts[2]

	// For most cases, we can try to find the parent by checking if there's a bridge
	// at bus 00 or look for the immediate parent in the PCI hierarchy

	// First, try to get parent from sysfs
	parentPath := buildSysBusPciPath(pciAddress, "../")
	parentDir, err := filepath.EvalSymlinks(parentPath)
	if err == nil {
		parentAddr := filepath.Base(parentDir)
		// Validate the parent address format
		if len(strings.Split(parentAddr, ":")) == 3 {
			return parentAddr, nil
		}
	}

	// Fallback: construct potential parent addresses
	// Try the root bus first (usually the PCIe root complex)
	deviceParts := strings.Split(deviceFunc, ".")
	if len(deviceParts) == 2 {
		// Try to find a bridge on bus 00
		parentAddr := fmt.Sprintf("%s:00:00.0", domain)
		parentDevPath := buildSysBusPciPath(parentAddr, "")
		if _, err := os.Stat(parentDevPath); err == nil {
			return parentAddr, nil
		}
	}

	// If we can't find a specific parent, return empty string
	return "", nil
}

// GetPCIeRoot returns the PCIe Root Complex for a given PCI device using the upstream Kubernetes implementation.
// The PCIe Root Complex is returned in the format "pci<domain>:<bus>" (e.g., "pci0000:00").
// This is used to identify devices that share the same PCIe Root Complex for resource alignment.
func (h *Host) GetPCIeRoot(pciAddress string) (string, error) {
	attr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pciAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get PCIe root for %s: %w", pciAddress, err)
	}

	// Extract the string value from the attribute
	if attr.Value.StringValue != nil {
		return *attr.Value.StringValue, nil
	}

	return "", fmt.Errorf("PCIe root attribute for %s has no string value", pciAddress)
}

// High-level Driver Management Functions

// BindDeviceDriver binds a device to the specified driver based on config.Driver:
// - If config.Driver == "", nothing is done
// - If config.Driver == "default", binds device to default driver
// - Otherwise, binds device to the specified driver
func (h *Host) BindDeviceDriver(pciAddress string, config *configapi.VfConfig) (string, error) {
	return h.BindDeviceDriverWithMAC(pciAddress, config, "")
}

// BindDeviceDriverWithMAC binds a device to the specified driver and optionally sets MAC address.
// For vfio-pci driver: MAC is set after unbind (if needed) but before bind to vfio-pci.
// This ensures VF has a network interface when MAC is configured.
func (h *Host) BindDeviceDriverWithMAC(pciAddress string, config *configapi.VfConfig, macAddress string) (string, error) {
	if config.Driver == "" {
		h.log.V(2).Info("BindDeviceDriverWithMAC(): no driver specified, skipping", "device", pciAddress)
		return "", nil
	}

	// Get current driver before making changes
	currentDriver, err := h.GetDriverByBusAndDevice(pciAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get current driver for device %s: %w", pciAddress, err)
	}

	if config.Driver == "default" {
		h.log.V(2).Info("BindDeviceDriverWithMAC(): binding device to default driver", "device", pciAddress)
		if err := h.BindDefaultDriver(pciAddress); err != nil {
			return "", fmt.Errorf("failed to bind device %s to default driver: %w", pciAddress, err)
		}
		return currentDriver, nil
	}

	// For vfio-pci with MAC: set MAC after unbind (if needed) but before bind
	if config.Driver == "vfio-pci" && macAddress != "" {
		// If already bound to something, unbind first
		if currentDriver != "" {
			h.log.V(2).Info("BindDeviceDriverWithMAC(): unbinding device before setting MAC", "device", pciAddress, "currentDriver", currentDriver)
			if err := h.UnbindDriverByBusAndDevice(pciAddress); err != nil {
				return "", fmt.Errorf("failed to unbind device %s: %w", pciAddress, err)
			}
		}

		// Now VF has network interface - set MAC
		h.log.V(2).Info("BindDeviceDriverWithMAC(): setting MAC before binding to vfio-pci", "device", pciAddress, "mac", macAddress)
		if err := h.SetVFMacAddress(pciAddress, macAddress); err != nil {
			h.log.Error(err, "BindDeviceDriverWithMAC(): failed to set MAC address, continuing without it", "device", pciAddress, "mac", macAddress)
			// Non-fatal: continue with bind
		}

		// Now bind to vfio-pci
		h.log.V(2).Info("BindDeviceDriverWithMAC(): binding device to vfio-pci", "device", pciAddress)
		if err := h.BindDriverByBusAndDevice(pciAddress, config.Driver); err != nil {
			return "", fmt.Errorf("failed to bind device %s to driver %s: %w", pciAddress, config.Driver, err)
		}
		return currentDriver, nil
	}

	// Normal case: no MAC to set, just bind
	h.log.V(2).Info("BindDeviceDriverWithMAC(): binding device to driver", "device", pciAddress, "driver", config.Driver)
	if err := h.BindDriverByBusAndDevice(pciAddress, config.Driver); err != nil {
		return "", fmt.Errorf("failed to bind device %s to driver %s: %w", pciAddress, config.Driver, err)
	}
	return currentDriver, nil
}

// RestoreDeviceDriver restores a device to its original driver
func (h *Host) RestoreDeviceDriver(pciAddress string, originalDriver string) error {
	if originalDriver == "" {
		h.log.V(2).Info("RestoreDeviceDriver(): no original driver, binding to default", "device", pciAddress)
		return h.BindDefaultDriver(pciAddress)
	}

	h.log.V(2).Info("RestoreDeviceDriver(): restoring device to original driver", "device", pciAddress, "driver", originalDriver)
	return h.BindDriverByBusAndDevice(pciAddress, originalDriver)
}

// BindDefaultDriver binds a device to its default driver
func (h *Host) BindDefaultDriver(pciAddress string) error {
	h.log.V(2).Info("BindDefaultDriver(): binding device to default driver", "device", pciAddress)

	curDriver, err := h.GetDriverByBusAndDevice(pciAddress)
	if err != nil {
		return err
	}
	if curDriver != "" {
		// If already bound to a non-DPDK driver, assume it's the default
		if !h.IsDpdkDriver(curDriver) {
			h.log.V(2).Info("BindDefaultDriver(): device already bound to default driver",
				"device", pciAddress, "driver", curDriver)
			return nil
		}
		if err := h.UnbindDriverByBusAndDevice(pciAddress); err != nil {
			return err
		}
	}
	if err := h.setDriverOverride(pciAddress, ""); err != nil {
		return err
	}
	if err := h.probeDriver(pciAddress); err != nil {
		return err
	}
	return nil
}

// Low-level Driver Operations

// BindDriverByBusAndDevice binds device to the provided driver
func (h *Host) BindDriverByBusAndDevice(device, driver string) error {
	h.log.V(2).Info("BindDriverByBusAndDevice(): bind device to driver",
		"device", device, "driver", driver)

	// Ensure DPDK kernel module is loaded before binding
	if err := h.EnsureDpdkModuleLoaded(driver); err != nil {
		return fmt.Errorf("failed to ensure DPDK module is loaded for driver %s: %w", driver, err)
	}

	curDriver, err := h.GetDriverByBusAndDevice(device)
	if err != nil {
		return err
	}
	if curDriver != "" {
		if curDriver == driver {
			h.log.V(2).Info("BindDriverByBusAndDevice(): device already bound to driver",
				"device", device, "driver", driver)
			return nil
		}
		if err := h.UnbindDriverByBusAndDevice(device); err != nil {
			return err
		}
	}
	if err := h.setDriverOverride(device, driver); err != nil {
		return err
	}
	if err := h.bindDriver(device, driver); err != nil {
		return err
	}
	return h.setDriverOverride(device, "")
}

// UnbindDriverByBusAndDevice unbinds device from its current driver
func (h *Host) UnbindDriverByBusAndDevice(device string) error {
	h.log.V(2).Info("UnbindDriverByBusAndDevice(): unbind device driver for device", "device", device)
	driver, err := h.GetDriverByBusAndDevice(device)
	if err != nil {
		return err
	}
	if driver == "" {
		h.log.V(2).Info("UnbindDriverByBusAndDevice(): device has no driver", "device", device)
		return nil
	}
	return h.unbindDriver(device, driver)
}

// GetDriverByBusAndDevice returns driver for device on the bus
func (h *Host) GetDriverByBusAndDevice(device string) (string, error) {
	driverLink := buildSysBusPciPath(device, "driver")
	driverInfo, err := os.Readlink(driverLink)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.log.V(2).Info("GetDriverByBusAndDevice(): driver path for device not exist", "device", device)
			return "", nil
		}
		h.log.Error(err, "GetDriverByBusAndDevice(): error getting driver info for device", "device", device)
		return "", err
	}
	h.log.V(2).Info("GetDriverByBusAndDevice(): driver for device", "device", device, "driver", driverInfo)
	return filepath.Base(driverInfo), nil
}

// Private helper methods

// bindDriver binds device to the provided driver
func (h *Host) bindDriver(device, driver string) error {
	h.log.V(2).Info("bindDriver(): bind to driver", "device", device, "driver", driver)
	bindPath := buildSysBusPciDriverPath(driver, "bind")
	err := os.WriteFile(bindPath, []byte(device), os.ModeAppend)
	if err != nil {
		h.log.Error(err, "bindDriver(): failed to bind driver", "device", device, "driver", driver)
		return err
	}
	return nil
}

// unbindDriver unbinds device from the driver
func (h *Host) unbindDriver(device, driver string) error {
	h.log.V(2).Info("unbindDriver(): unbind from driver", "device", device, "driver", driver)
	unbindPath := buildSysBusPciDriverPath(driver, "unbind")
	err := os.WriteFile(unbindPath, []byte(device), os.ModeAppend)
	if err != nil {
		h.log.Error(err, "unbindDriver(): failed to unbind driver", "device", device, "driver", driver)
		return err
	}
	return nil
}

// probeDriver probes driver for device on the bus
func (h *Host) probeDriver(device string) error {
	h.log.V(2).Info("probeDriver(): drivers probe", "device", device)
	probePath := buildSysPath("/sys/bus/pci/drivers_probe")
	err := os.WriteFile(probePath, []byte(device), os.ModeAppend)
	if err != nil {
		h.log.Error(err, "probeDriver(): failed to trigger driver probe", "device", device)
		return err
	}
	return nil
}

// setDriverOverride sets driver override for the bus/device,
// resets override if override arg is "",
// if device doesn't support overriding (has no driver_override path), does nothing
func (h *Host) setDriverOverride(device, override string) error {
	driverOverridePath := buildSysBusPciPath(device, "driver_override")
	if _, err := os.Stat(driverOverridePath); err != nil {
		if os.IsNotExist(err) {
			h.log.V(2).Info("setDriverOverride(): device doesn't support driver override, skip", "device", device)
			return nil
		}
		return err
	}
	var overrideData []byte
	if override != "" {
		h.log.V(2).Info("setDriverOverride(): configure driver override for device", "device", device, "driver", override)
		overrideData = []byte(override)
	} else {
		h.log.V(2).Info("setDriverOverride(): reset driver override for device", "device", device)
		overrideData = []byte("\x00")
	}
	err := os.WriteFile(driverOverridePath, overrideData, os.ModeAppend)
	if err != nil {
		h.log.Error(err, "setDriverOverride(): fail to write driver_override for device",
			"device", device, "driver", override)
		return err
	}
	return nil
}

// Utility Functions

// IsDpdkDriver checks if the given driver is a DPDK driver
func (h *Host) IsDpdkDriver(driver string) bool {
	dpdkDrivers := []string{"vfio-pci", "uio_pci_generic", "igb_uio"}
	for _, dpdkDriver := range dpdkDrivers {
		if driver == dpdkDriver {
			return true
		}
	}
	return false
}

// VFIO Device Functions

// GetVFIODeviceFile returns VFIO device files for vfio-pci bound PCI device's PCI address
func (h *Host) GetVFIODeviceFile(pciAddress string) (devFileHost, devFileContainer string, err error) {
	h.log.V(2).Info("GetVFIODeviceFile(): getting VFIO device file", "device", pciAddress)

	const devDir = "/dev"

	// Get iommu group for this device
	devPath := buildSysBusPciPath(pciAddress, "")
	_, err = os.Lstat(devPath)
	if err != nil {
		h.log.Error(err, "GetVFIODeviceFile(): Could not get directory information for device", "device", pciAddress)
		err = fmt.Errorf("GetVFIODeviceFile(): Could not get directory information for device: %s, Err: %v", pciAddress, err)
		return devFileHost, devFileContainer, err
	}

	iommuDir := filepath.Join(devPath, "iommu_group")
	h.log.V(2).Info("GetVFIODeviceFile(): checking iommu_group", "device", pciAddress, "iommuDir", iommuDir)

	dirInfo, err := os.Lstat(iommuDir)
	if err != nil {
		h.log.Error(err, "GetVFIODeviceFile(): unable to find iommu_group", "device", pciAddress)
		err = fmt.Errorf("GetVFIODeviceFile(): unable to find iommu_group %v", err)
		return devFileHost, devFileContainer, err
	}

	if dirInfo.Mode()&os.ModeSymlink == 0 {
		h.log.Error(nil, "GetVFIODeviceFile(): invalid symlink to iommu_group", "device", pciAddress)
		err = fmt.Errorf("GetVFIODeviceFile(): invalid symlink to iommu_group %v", err)
		return devFileHost, devFileContainer, err
	}

	linkName, err := filepath.EvalSymlinks(iommuDir)
	if err != nil {
		h.log.Error(err, "GetVFIODeviceFile(): error reading symlink to iommu_group", "device", pciAddress)
		err = fmt.Errorf("GetVFIODeviceFile(): error reading symlink to iommu_group %v", err)
		return devFileHost, devFileContainer, err
	}
	devFileContainer = filepath.Join(devDir, "vfio", filepath.Base(linkName))
	devFileHost = devFileContainer

	h.log.V(2).Info("GetVFIODeviceFile(): resolved iommu group", "device", pciAddress, "iommuGroup", filepath.Base(linkName))

	// Get a file path to the iommu group name
	namePath := filepath.Join(linkName, "name")
	// Read the iommu group name
	// The name file will not exist on baremetal
	vfioName, errName := os.ReadFile(namePath)
	if errName == nil {
		vName := strings.TrimSpace(string(vfioName))
		h.log.V(2).Info("GetVFIODeviceFile(): read iommu group name", "device", pciAddress, "vfioName", vName)

		// if the iommu group name == vfio-noiommu then we are in a VM, adjust path to vfio device
		if vName == "vfio-noiommu" {
			h.log.V(2).Info("GetVFIODeviceFile(): detected vfio-noiommu mode, adjusting device path", "device", pciAddress)
			linkName = filepath.Join(filepath.Dir(linkName), "noiommu-"+filepath.Base(linkName))
			devFileHost = filepath.Join(devDir, "vfio", filepath.Base(linkName))
		}
	} else {
		h.log.V(2).Info("GetVFIODeviceFile(): iommu group name file not found (baremetal mode)", "device", pciAddress)
	}

	h.log.V(2).Info("GetVFIODeviceFile(): successfully resolved VFIO device files",
		"device", pciAddress, "devFileHost", devFileHost, "devFileContainer", devFileContainer)

	return devFileHost, devFileContainer, err
}

// Kernel Module Management Functions

// IsKernelModuleLoaded checks if a kernel module is currently loaded
func (h *Host) IsKernelModuleLoaded(moduleName string) bool {
	// Read /proc/modules to check if the module is loaded
	content, err := os.ReadFile(buildProcPath("/proc/modules"))
	if err != nil {
		h.log.Error(err, "IsKernelModuleLoaded(): failed to read /proc/modules")
		return false
	}

	// Each line in /proc/modules starts with the module name
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, moduleName+" ") || line == moduleName {
			h.log.V(2).Info("IsKernelModuleLoaded(): module is loaded", "module", moduleName)
			return true
		}
	}

	h.log.V(2).Info("IsKernelModuleLoaded(): module is not loaded", "module", moduleName)
	return false
}

// LoadKernelModule loads a kernel module using modprobe with chroot to access host filesystem
func (h *Host) LoadKernelModule(moduleName string) error {
	h.log.V(2).Info("LoadKernelModule(): loading kernel module", "module", moduleName)

	cmd := exec.Command("chroot", "/proc/1/root", "modprobe", moduleName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		h.log.Error(err, "LoadKernelModule(): failed to load kernel module",
			"module", moduleName, "output", string(output))
		return fmt.Errorf("failed to load kernel module %s: %w (output: %s)",
			moduleName, err, string(output))
	}

	h.log.V(2).Info("LoadKernelModule(): successfully loaded kernel module", "module", moduleName)
	return nil
}

// EnsureDpdkModuleLoaded ensures that the kernel module for a DPDK driver is loaded
func (h *Host) EnsureDpdkModuleLoaded(driver string) error {
	if !h.IsDpdkDriver(driver) {
		h.log.V(2).Info("EnsureDpdkModuleLoaded(): driver is not a DPDK driver, skipping module check", "driver", driver)
		return nil
	}

	// Map DPDK driver names to their corresponding kernel module names
	var modulesNames []string
	switch driver {
	case "vfio-pci":
		modulesNames = []string{"vfio", "vfio_pci"}
	default:
		return fmt.Errorf("unknown DPDK driver: %s", driver)
	}

	// Check which modules need to be loaded
	var modulesToLoad []string
	for _, moduleName := range modulesNames {
		if h.IsKernelModuleLoaded(moduleName) {
			h.log.V(2).Info("EnsureDpdkModuleLoaded(): kernel module already loaded", "driver", driver, "module", moduleName)
		} else {
			modulesToLoad = append(modulesToLoad, moduleName)
		}
	}

	// If all modules are already loaded, return early
	if len(modulesToLoad) == 0 {
		h.log.V(2).Info("EnsureDpdkModuleLoaded(): all required modules already loaded", "driver", driver)
		return nil
	}

	// Load missing modules
	var errors []error
	for _, moduleName := range modulesToLoad {
		h.log.Info("EnsureDpdkModuleLoaded(): loading kernel module for DPDK driver", "driver", driver, "module", moduleName)
		if err := h.LoadKernelModule(moduleName); err != nil {
			h.log.Error(err, "EnsureDpdkModuleLoaded(): failed to load module", "driver", driver, "module", moduleName)
			errors = append(errors, fmt.Errorf("failed to load module %s: %w", moduleName, err))
			continue
		}

		// Verify module was loaded successfully
		if !h.IsKernelModuleLoaded(moduleName) {
			err := fmt.Errorf("module %s was not loaded after LoadKernelModule call", moduleName)
			h.log.Error(err, "EnsureDpdkModuleLoaded(): module verification failed", "driver", driver, "module", moduleName)
			errors = append(errors, err)
		} else {
			h.log.Info("EnsureDpdkModuleLoaded(): successfully loaded kernel module", "driver", driver, "module", moduleName)
		}
	}

	// If we encountered any errors, return them
	if len(errors) > 0 {
		return fmt.Errorf("failed to load %d out of %d required kernel modules for DPDK driver %s: %v", len(errors), len(modulesToLoad), driver, errors)
	}
	return nil
}

// EnsureVhostModulesLoaded ensures that the tun and vhost_net kernel modules are loaded
func (h *Host) EnsureVhostModulesLoaded() error {
	// Modules required for vhost functionality
	modulesNames := []string{"tun", "vhost_net"}

	// Check which modules need to be loaded
	var modulesToLoad []string
	for _, moduleName := range modulesNames {
		if h.IsKernelModuleLoaded(moduleName) {
			h.log.V(2).Info("EnsureVhostModulesLoaded(): kernel module already loaded", "module", moduleName)
		} else {
			modulesToLoad = append(modulesToLoad, moduleName)
		}
	}

	// If all modules are already loaded, return early
	if len(modulesToLoad) == 0 {
		h.log.V(2).Info("EnsureVhostModulesLoaded(): all required vhost modules already loaded")
		return nil
	}

	// Load missing modules
	var errors []error
	for _, moduleName := range modulesToLoad {
		h.log.Info("EnsureVhostModulesLoaded(): loading kernel module for vhost functionality", "module", moduleName)
		if err := h.LoadKernelModule(moduleName); err != nil {
			h.log.Error(err, "EnsureVhostModulesLoaded(): failed to load module", "module", moduleName)
			errors = append(errors, fmt.Errorf("failed to load module %s: %w", moduleName, err))
			continue
		}

		// Verify module was loaded successfully
		if !h.IsKernelModuleLoaded(moduleName) {
			err := fmt.Errorf("module %s was not loaded after LoadKernelModule call", moduleName)
			h.log.Error(err, "EnsureVhostModulesLoaded(): module verification failed", "module", moduleName)
			errors = append(errors, err)
		} else {
			h.log.Info("EnsureVhostModulesLoaded(): successfully loaded kernel module", "module", moduleName)
		}
	}

	// If we encountered any errors, return them
	if len(errors) > 0 {
		return fmt.Errorf("failed to load %d out of %d required kernel modules for vhost functionality: %v", len(errors), len(modulesToLoad), errors)
	}
	return nil
}

// SetVFMacAddress sets the MAC address on a VF via the PF
// This must be called BEFORE binding the VF to vfio-pci, as vfio-bound devices
// don't have network interfaces and cannot have their MAC addresses set.
func (h *Host) SetVFMacAddress(vfPciAddress string, macAddress string) error {
	h.log.V(2).Info("SetVFMacAddress", "vfPciAddress", vfPciAddress, "macAddress", macAddress)

	// Get the PF for this VF
	pfPciAddress, err := h.GetParentPciAddress(vfPciAddress)
	if err != nil {
		return fmt.Errorf("failed to get parent PF for VF %s: %w", vfPciAddress, err)
	}

	// Get the PF network interface name
	pfNetdev := h.TryGetInterfaceName(pfPciAddress)
	if pfNetdev == "" {
		return fmt.Errorf("failed to get network interface for PF %s", pfPciAddress)
	}

	// Get VF list to find the VF ID
	vfList, err := h.GetVFList(pfPciAddress)
	if err != nil {
		return fmt.Errorf("failed to get VF list for PF %s: %w", pfPciAddress, err)
	}

	// Find the VF ID for this VF PCI address
	vfID := -1
	for _, vf := range vfList {
		if vf.PciAddress == vfPciAddress {
			vfID = vf.VFID
			break
		}
	}

	if vfID == -1 {
		return fmt.Errorf("VF %s not found in PF %s VF list", vfPciAddress, pfPciAddress)
	}

	// Set the MAC address via ip link command
	// Equivalent to: ip link set <pf> vf <vf_id> mac <mac>
	cmd := exec.Command("ip", "link", "set", pfNetdev, "vf", strconv.Itoa(vfID), "mac", macAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set MAC address on VF %d of PF %s: %w, output: %s", vfID, pfNetdev, err, string(output))
	}

	h.log.V(2).Info("Successfully set MAC address on VF", "pf", pfNetdev, "vfID", vfID, "mac", macAddress)
	return nil
}
