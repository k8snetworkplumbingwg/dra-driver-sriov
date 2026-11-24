package v1alpha1

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

var driverIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

// ValidateInterfaceName checks explicit or generated Linux interface names.
func ValidateInterfaceName(name string) error {
	if name == "" || len(name) > 15 || name == "." || name == ".." || strings.ContainsAny(name, "/:\x00") || strings.ContainsAny(name, " \t\n\r\v\f") {
		return fmt.Errorf("invalid Linux interface name %q", name)
	}
	return nil
}

// validateNetAttachDefPairing rejects a NAD namespace without a name.
func validateNetAttachDefPairing(c *VfConfig) error {
	if c.NetAttachDefNamespace != "" && c.NetAttachDefName == "" {
		return fmt.Errorf("netAttachDefNamespace requires netAttachDefName")
	}
	return nil
}

// ValidateStandaloneNetAttachDef requires both NAD fields except for VFIO without CNI.
func ValidateStandaloneNetAttachDef(c *VfConfig) error {
	if c.Driver != "vfio-pci" || c.NetAttachDefName != "" || c.NetAttachDefNamespace != "" {
		if c.NetAttachDefName == "" || c.NetAttachDefNamespace == "" {
			return fmt.Errorf("standalone attachment requires netAttachDefName and netAttachDefNamespace")
		}
	}
	return nil
}

// ValidateForPrepare checks configuration fields and mode-specific NAD requirements.
func ValidateForPrepare(configurationMode string, c *VfConfig) error {
	if c == nil {
		return fmt.Errorf("VfConfig is nil")
	}
	if c.Driver != "" && !driverIdentifier.MatchString(c.Driver) {
		return fmt.Errorf("driver must be a single identifier, not a path")
	}
	if c.IfName != "" {
		if err := ValidateInterfaceName(c.IfName); err != nil {
			return err
		}
	}
	if c.NetAttachDefName != "" && len(validation.IsDNS1123Subdomain(c.NetAttachDefName)) != 0 {
		return fmt.Errorf("invalid netAttachDefName %q", c.NetAttachDefName)
	}
	if c.NetAttachDefNamespace != "" && len(validation.IsDNS1123Label(c.NetAttachDefNamespace)) != 0 {
		return fmt.Errorf("invalid netAttachDefNamespace %q", c.NetAttachDefNamespace)
	}
	if err := validateNetAttachDefPairing(c); err != nil {
		return err
	}

	switch consts.ConfigurationMode(configurationMode) {
	case consts.ConfigurationModeMultus:
		return nil
	case consts.ConfigurationModeStandalone:
		return ValidateStandaloneNetAttachDef(c)
	default:
		return fmt.Errorf("unsupported configuration mode %q", configurationMode)
	}
}

// Validate ensures that GpuConfig has a valid set of values.
func (c *VfConfig) Validate() error {
	if c.Driver == "" {
		return fmt.Errorf("no driver set")
	}
	if c.NetAttachDefName == "" {
		return fmt.Errorf("no net attach def name set")
	}

	return nil
}
