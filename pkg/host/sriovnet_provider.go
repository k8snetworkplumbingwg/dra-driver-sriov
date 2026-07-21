package host

import "github.com/k8snetworkplumbingwg/sriovnet"

// SriovnetProvider wraps sriovnet library calls to allow mocking in unit tests.
type SriovnetProvider interface {
	// GetUplinkRepresentor returns the PF uplink netdev name for a given PCI
	// address (PF or VF).
	GetUplinkRepresentor(pciAddr string) (string, error)
	// GetVfRepresentor returns the host-side switchdev representor netdev name
	// for the VF at vfIndex under the given uplink (a PF PCI address or netdev
	// name). Only meaningful in switchdev mode.
	GetVfRepresentor(uplink string, vfIndex int) (string, error)
}

type defaultSriovnetProvider struct{}

var _ SriovnetProvider = &defaultSriovnetProvider{}

func (defaultSriovnetProvider) GetUplinkRepresentor(pciAddr string) (string, error) {
	return sriovnet.GetUplinkRepresentor(pciAddr)
}

func (defaultSriovnetProvider) GetVfRepresentor(uplink string, vfIndex int) (string, error) {
	return sriovnet.GetVfRepresentor(uplink, vfIndex)
}
