package host

import (
	"fmt"

	"github.com/vishvananda/netlink"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
)

// NetlinkProvider wraps netlink library calls to allow mocking in unit tests.
type NetlinkProvider interface {
	// GetDevLinkDeviceEswitchMode returns the eswitch mode ("legacy" or
	// "switchdev") for the given PF PCI address via devlink.
	GetDevLinkDeviceEswitchMode(pciAddr string) (string, error)

	// SetVfVlanQosProto sets the VLAN id, QoS priority and VLAN protocol
	// (802.1q/802.1ad, for QinQ) of a VF on the given PF interface.
	SetVfVlanQosProto(pfName string, vf, vlan, qos int, proto string) error
	// SetVfSpoofchk enables or disables spoof checking for a VF.
	SetVfSpoofchk(pfName string, vf int, enabled bool) error
	// SetVfTrust enables or disables trusted mode for a VF.
	SetVfTrust(pfName string, vf int, enabled bool) error
	// SetVfRate sets the min and max tx rate (Mbps) for a VF.
	SetVfRate(pfName string, vf, minRate, maxRate int) error
	// SetVfState sets the administrative link state ("auto"/"enable"/"disable") of a VF.
	SetVfState(pfName string, vf int, state string) error
	// GetVfLinkConfig reads the current native link attributes of a VF. Every
	// field of the returned config is populated.
	GetVfLinkConfig(pfName string, vf int) (*configapi.VFLinkConfig, error)
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

func (defaultNetlinkProvider) SetVfVlanQosProto(pfName string, vf, vlan, qos int, proto string) error {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return fmt.Errorf("failed to get PF link %q: %w", pfName, err)
	}
	nlProto, err := vlanProtoToNetlink(proto)
	if err != nil {
		return err
	}
	return netlink.LinkSetVfVlanQosProto(link, vf, vlan, qos, nlProto)
}

func (defaultNetlinkProvider) SetVfSpoofchk(pfName string, vf int, enabled bool) error {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return fmt.Errorf("failed to get PF link %q: %w", pfName, err)
	}
	return netlink.LinkSetVfSpoofchk(link, vf, enabled)
}

func (defaultNetlinkProvider) SetVfTrust(pfName string, vf int, enabled bool) error {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return fmt.Errorf("failed to get PF link %q: %w", pfName, err)
	}
	return netlink.LinkSetVfTrust(link, vf, enabled)
}

func (defaultNetlinkProvider) SetVfRate(pfName string, vf, minRate, maxRate int) error {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return fmt.Errorf("failed to get PF link %q: %w", pfName, err)
	}
	return netlink.LinkSetVfRate(link, vf, minRate, maxRate)
}

func (defaultNetlinkProvider) SetVfState(pfName string, vf int, state string) error {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return fmt.Errorf("failed to get PF link %q: %w", pfName, err)
	}
	nlState, err := linkStateToNetlink(state)
	if err != nil {
		return err
	}
	return netlink.LinkSetVfState(link, vf, nlState)
}

func (defaultNetlinkProvider) GetVfLinkConfig(pfName string, vf int) (*configapi.VFLinkConfig, error) {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return nil, fmt.Errorf("failed to get PF link %q: %w", pfName, err)
	}
	for i := range link.Attrs().Vfs {
		info := link.Attrs().Vfs[i]
		if info.ID != vf {
			continue
		}
		proto, err := netlinkToVlanProto(info.VlanProto)
		if err != nil {
			return nil, fmt.Errorf("VF %d of %s: %w", vf, pfName, err)
		}
		state, err := netlinkToLinkState(info.LinkState)
		if err != nil {
			return nil, fmt.Errorf("VF %d of %s: %w", vf, pfName, err)
		}
		vlan := info.Vlan
		qos := info.Qos
		spoofChk := info.Spoofchk
		trust := info.Trust != 0
		minTxRate := int(info.MinTxRate)
		maxTxRate := int(info.MaxTxRate)
		return &configapi.VFLinkConfig{
			VLAN:      &vlan,
			Qos:       &qos,
			VlanProto: &proto,
			SpoofChk:  &spoofChk,
			Trust:     &trust,
			MinTxRate: &minTxRate,
			MaxTxRate: &maxTxRate,
			LinkState: &state,
		}, nil
	}
	return nil, fmt.Errorf("VF %d not found on PF %s", vf, pfName)
}

// vlanProtoToNetlink maps a VLAN protocol string to its netlink constant.
// An empty string defaults to 802.1q.
func vlanProtoToNetlink(proto string) (int, error) {
	switch proto {
	case "", configapi.VlanProto8021q:
		return int(netlink.VLAN_PROTOCOL_8021Q), nil
	case configapi.VlanProto8021ad:
		return int(netlink.VLAN_PROTOCOL_8021AD), nil
	default:
		return 0, fmt.Errorf("unsupported vlan protocol %q", proto)
	}
}

// netlinkToVlanProto maps a netlink VLAN protocol constant back to its string.
// An unknown protocol maps to 802.1q, which is the kernel default.
func netlinkToVlanProto(proto int) (string, error) {
	switch netlink.VlanProtocol(proto) {
	case netlink.VLAN_PROTOCOL_UNKNOWN, netlink.VLAN_PROTOCOL_8021Q:
		return configapi.VlanProto8021q, nil
	case netlink.VLAN_PROTOCOL_8021AD:
		return configapi.VlanProto8021ad, nil
	default:
		return "", fmt.Errorf("unsupported vlan protocol %d reported by the kernel", proto)
	}
}

// netlinkToLinkState maps a netlink VF link state constant back to its string.
func netlinkToLinkState(state uint32) (string, error) {
	switch state {
	case netlink.VF_LINK_STATE_AUTO:
		return configapi.LinkStateAuto, nil
	case netlink.VF_LINK_STATE_ENABLE:
		return configapi.LinkStateEnable, nil
	case netlink.VF_LINK_STATE_DISABLE:
		return configapi.LinkStateDisable, nil
	default:
		return "", fmt.Errorf("unsupported vf link state %d reported by the kernel", state)
	}
}

// linkStateToNetlink maps a VF link state string to its netlink constant.
func linkStateToNetlink(state string) (uint32, error) {
	switch state {
	case configapi.LinkStateAuto:
		return netlink.VF_LINK_STATE_AUTO, nil
	case configapi.LinkStateEnable:
		return netlink.VF_LINK_STATE_ENABLE, nil
	case configapi.LinkStateDisable:
		return netlink.VF_LINK_STATE_DISABLE, nil
	default:
		return 0, fmt.Errorf("unsupported vf link state %q", state)
	}
}
