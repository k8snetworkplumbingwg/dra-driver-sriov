package v1alpha1

import (
	"testing"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

func TestValidateForPrepareStandaloneNADPairing(t *testing.T) {
	cfg := &VfConfig{NetAttachDefName: "net1"}
	err := ValidateForPrepare(string(consts.ConfigurationModeStandalone), cfg)
	if err == nil {
		t.Fatal("expected error when NAD namespace is missing")
	}

	cfg = &VfConfig{
		NetAttachDefName:      "net1",
		NetAttachDefNamespace: "admin",
	}
	if err := ValidateForPrepare(string(consts.ConfigurationModeStandalone), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateForPrepareMultusPermissive(t *testing.T) {
	cfg := &VfConfig{NetAttachDefName: "net1"}
	if err := ValidateForPrepare(string(consts.ConfigurationModeMultus), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateForPrepareInvalidFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		config *VfConfig
	}{
		{"nil config", nil},
		{"driver path", &VfConfig{Driver: "../../vfio-pci"}},
		{"invalid interface", &VfConfig{IfName: "net/1"}},
		{"long interface", &VfConfig{IfName: "interface-name-too-long"}},
		{"namespace without name", &VfConfig{NetAttachDefNamespace: "admin"}},
		{"invalid NAD namespace", &VfConfig{NetAttachDefName: "blue-net", NetAttachDefNamespace: "admin/other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateForPrepare(string(consts.ConfigurationModeMultus), test.config); err == nil {
				t.Fatal("expected invalid configuration to be rejected")
			}
		})
	}
}
