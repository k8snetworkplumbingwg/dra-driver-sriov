package host

import (
	"context"
	"fmt"
	"os"
	"path"

	"k8s.io/klog/v2"
)

// NewHostForTest creates a Host with injectable providers, for use in unit tests.
// Pass nil for a provider to use the default production implementation.
// The optional sriovnetProvider overrides the sriovnet calls (useful for
// TryGetPFInterfaceName tests); when omitted the default sriovnet library is used.
func NewHostForTest(netlinkProvider NetlinkProvider, sriovnetProvider ...SriovnetProvider) Interface {
	if netlinkProvider == nil {
		netlinkProvider = &defaultNetlinkProvider{}
	}
	snProvider := SriovnetProvider(&defaultSriovnetProvider{})
	if len(sriovnetProvider) > 0 && sriovnetProvider[0] != nil {
		snProvider = sriovnetProvider[0]
	}
	return &Host{
		log:              klog.FromContext(context.Background()).WithName("Host"),
		rdmaProvider:     newRdmaProvider(),
		netlinkProvider:  netlinkProvider,
		sriovnetProvider: snProvider,
	}
}

// FakeNetlinkProvider is a configurable NetlinkProvider for use in unit tests.
// It records the arguments of the VF setter calls so tests can assert on how
// VFLinkConfig fields map to netlink calls.
type FakeNetlinkProvider struct {
	EswitchMode  string
	EswitchError error

	// Recorded calls to the VF setters, in the order they were invoked.
	VlanCalls     []VlanCall
	SpoofchkCalls []SpoofchkCall
	TrustCalls    []TrustCall
	RateCalls     []RateCall
	StateCalls    []StateCall

	// Optional errors to return from the corresponding setter.
	VlanErr     error
	SpoofchkErr error
	TrustErr    error
	RateErr     error
	StateErr    error
}

// VlanCall records a SetVfVlanQosProto invocation.
type VlanCall struct {
	PF    string
	VF    int
	Vlan  int
	Qos   int
	Proto string
}

// SpoofchkCall records a SetVfSpoofchk invocation.
type SpoofchkCall struct {
	PF      string
	VF      int
	Enabled bool
}

// TrustCall records a SetVfTrust invocation.
type TrustCall struct {
	PF      string
	VF      int
	Enabled bool
}

// RateCall records a SetVfRate invocation.
type RateCall struct {
	PF      string
	VF      int
	MinRate int
	MaxRate int
}

// StateCall records a SetVfState invocation.
type StateCall struct {
	PF    string
	VF    int
	State string
}

func (f *FakeNetlinkProvider) GetDevLinkDeviceEswitchMode(_ string) (string, error) {
	return f.EswitchMode, f.EswitchError
}

func (f *FakeNetlinkProvider) SetVfVlanQosProto(pfName string, vf, vlan, qos int, proto string) error {
	f.VlanCalls = append(f.VlanCalls, VlanCall{PF: pfName, VF: vf, Vlan: vlan, Qos: qos, Proto: proto})
	return f.VlanErr
}

func (f *FakeNetlinkProvider) SetVfSpoofchk(pfName string, vf int, enabled bool) error {
	f.SpoofchkCalls = append(f.SpoofchkCalls, SpoofchkCall{PF: pfName, VF: vf, Enabled: enabled})
	return f.SpoofchkErr
}

func (f *FakeNetlinkProvider) SetVfTrust(pfName string, vf int, enabled bool) error {
	f.TrustCalls = append(f.TrustCalls, TrustCall{PF: pfName, VF: vf, Enabled: enabled})
	return f.TrustErr
}

func (f *FakeNetlinkProvider) SetVfRate(pfName string, vf, minRate, maxRate int) error {
	f.RateCalls = append(f.RateCalls, RateCall{PF: pfName, VF: vf, MinRate: minRate, MaxRate: maxRate})
	return f.RateErr
}

func (f *FakeNetlinkProvider) SetVfState(pfName string, vf int, state string) error {
	f.StateCalls = append(f.StateCalls, StateCall{PF: pfName, VF: vf, State: state})
	return f.StateErr
}

// FakeSriovnetProvider is a configurable SriovnetProvider for use in unit tests.
type FakeSriovnetProvider struct {
	// UplinkName is returned by GetUplinkRepresentor on success.
	UplinkName string
	// UplinkError, when non-nil, is returned instead of UplinkName.
	UplinkError error
}

func (f *FakeSriovnetProvider) GetUplinkRepresentor(_ string) (string, error) {
	return f.UplinkName, f.UplinkError
}

// FakeFilesystem allows to setup isolated fake files structure used for the tests.
type FakeFilesystem struct {
	RootDir  string
	Dirs     []string
	Files    map[string][]byte
	Symlinks map[string]string
}

// Use function creates entire files structure and returns a function to tear it down. Example usage: defer fs.Use()()
func (fs *FakeFilesystem) Use() func() {
	// create the new fake fs root dir in /tmp/sriov...
	tmpDir, err := os.MkdirTemp("", "sriov")
	if err != nil {
		panic(fmt.Errorf("error creating fake root dir: %s", err.Error()))
	}
	fs.RootDir = tmpDir

	for _, dir := range fs.Dirs {
		//nolint: mnd,gosec
		err := os.MkdirAll(path.Join(fs.RootDir, dir), 0755)
		if err != nil {
			panic(fmt.Errorf("error creating fake directory: %s", err.Error()))
		}
	}
	for filename, body := range fs.Files {
		//nolint: mnd
		err := os.WriteFile(path.Join(fs.RootDir, filename), body, 0600)
		if err != nil {
			panic(fmt.Errorf("error creating fake file: %s", err.Error()))
		}
	}
	//nolint: mnd,gosec
	err = os.MkdirAll(path.Join(fs.RootDir, "usr/share/hwdata"), 0755)
	if err != nil {
		panic(fmt.Errorf("error creating fake directory: %s", err.Error()))
	}
	//nolint: mnd,gosec
	err = os.MkdirAll(path.Join(fs.RootDir, "var/run/cdi"), 0755)
	if err != nil {
		panic(fmt.Errorf("error creating fake cdi directory: %s", err.Error()))
	}

	for link, target := range fs.Symlinks {
		err = os.Symlink(target, path.Join(fs.RootDir, link))
		if err != nil {
			panic(fmt.Errorf("error creating fake symlink: %s", err.Error()))
		}
	}

	RootDir = fs.RootDir

	return func() {
		// remove temporary fake fs
		err := os.RemoveAll(fs.RootDir)
		if err != nil {
			panic(fmt.Errorf("error tearing down fake filesystem: %s", err.Error()))
		}
	}
}
