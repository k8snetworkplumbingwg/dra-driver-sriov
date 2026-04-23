package host

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

// NetlinkProvider wraps netlink library calls to allow mocking in unit tests.
type NetlinkProvider interface {
	// GetDevLinkDeviceEswitchMode returns the eswitch mode ("legacy" or
	// "switchdev") for the given PF PCI address via devlink.
	GetDevLinkDeviceEswitchMode(pciAddr string) (string, error)

	// IsDevlinkPhysicalPort reports whether the given netdev name is a devlink
	// port with DEVLINK_PORT_FLAVOR_PHYSICAL.
	// An error is returned when devlink is unavailable or the port is not found
	// in the devlink port list; the caller should then trust sysfs alone.
	IsDevlinkPhysicalPort(netdev string) (bool, error)
}

type defaultNetlinkProvider struct{}

var _ NetlinkProvider = &defaultNetlinkProvider{}

func (defaultNetlinkProvider) GetDevLinkDeviceEswitchMode(pciAddr string) (string, error) {
	dev, err := netlink.DevLinkGetDeviceByName("pci", pciAddr)
	if err != nil {
		return "", err
	}
	return dev.Attrs.Eswitch.Mode, nil
}

func (defaultNetlinkProvider) IsDevlinkPhysicalPort(netdev string) (bool, error) {
	ports, err := netlink.DevLinkGetAllPortList()
	if err != nil {
		return false, fmt.Errorf("failed to list devlink ports: %w", err)
	}
	for _, port := range ports {
		if port.NetdeviceName == netdev {
			return port.PortFlavour == nl.DEVLINK_PORT_FLAVOUR_PHYSICAL, nil //nolint:misspell
		}
	}
	return false, fmt.Errorf("devlink port not found for netdev %q", netdev)
}
