package v1alpha1

import "fmt"

// VF attribute bounds and allowed values.
const (
	vlanMin = 0
	vlanMax = 4094
	qosMin  = 0
	qosMax  = 7

	VlanProto8021q  = "802.1q"
	VlanProto8021ad = "802.1ad"

	LinkStateAuto    = "auto"
	LinkStateEnable  = "enable"
	LinkStateDisable = "disable"
)

// Validate ensures that GpuConfig has a valid set of values.
func (c *VfConfig) Validate() error {
	if c.Driver == "" {
		return fmt.Errorf("no driver set")
	}
	if c.NetAttachDefName == "" {
		return fmt.Errorf("no net attach def name set")
	}
	if c.VF != nil {
		if err := c.VF.validate(); err != nil {
			return err
		}
	}

	return nil
}

// validate checks the native VF link attributes for valid ranges and values.
// Only non-nil (explicitly requested) fields are validated.
func (v *VFLinkConfig) validate() error {
	if v.VLAN != nil && (*v.VLAN < vlanMin || *v.VLAN > vlanMax) {
		return fmt.Errorf("vlan %d out of range [%d-%d]", *v.VLAN, vlanMin, vlanMax)
	}
	if v.Qos != nil && (*v.Qos < qosMin || *v.Qos > qosMax) {
		return fmt.Errorf("qos %d out of range [%d-%d]", *v.Qos, qosMin, qosMax)
	}
	if v.VlanProto != nil {
		switch *v.VlanProto {
		case VlanProto8021q, VlanProto8021ad:
		default:
			return fmt.Errorf("invalid vlanProto %q, expected %q or %q", *v.VlanProto, VlanProto8021q, VlanProto8021ad)
		}
	}
	if v.LinkState != nil {
		switch *v.LinkState {
		case LinkStateAuto, LinkStateEnable, LinkStateDisable:
		default:
			return fmt.Errorf("invalid linkState %q, expected %q, %q or %q", *v.LinkState, LinkStateAuto, LinkStateEnable, LinkStateDisable)
		}
	}
	if v.MinTxRate != nil && *v.MinTxRate < 0 {
		return fmt.Errorf("minTxRate %d must not be negative", *v.MinTxRate)
	}
	if v.MaxTxRate != nil && *v.MaxTxRate < 0 {
		return fmt.Errorf("maxTxRate %d must not be negative", *v.MaxTxRate)
	}
	if v.MinTxRate != nil && v.MaxTxRate != nil && *v.MinTxRate > *v.MaxTxRate {
		return fmt.Errorf("minTxRate %d must not exceed maxTxRate %d", *v.MinTxRate, *v.MaxTxRate)
	}
	return nil
}
