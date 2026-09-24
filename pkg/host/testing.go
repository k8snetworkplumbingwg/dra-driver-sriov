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
type FakeNetlinkProvider struct {
	EswitchMode  string
	EswitchError error
}

func (f *FakeNetlinkProvider) GetDevLinkDeviceEswitchMode(_ string) (string, error) {
	return f.EswitchMode, f.EswitchError
}

// FakeSriovnetProvider is a configurable SriovnetProvider for use in unit tests.
type FakeSriovnetProvider struct {
	// UplinkName is returned by GetUplinkRepresentor on success.
	UplinkName string
	// UplinkError, when non-nil, is returned instead of UplinkName.
	UplinkError error
	// RepresentorName is returned by GetVfRepresentor on success.
	RepresentorName string
	// RepresentorError, when non-nil, is returned instead of RepresentorName.
	RepresentorError error
}

func (f *FakeSriovnetProvider) GetUplinkRepresentor(_ string) (string, error) {
	return f.UplinkName, f.UplinkError
}

func (f *FakeSriovnetProvider) GetVfRepresentor(_ string, _ int) (string, error) {
	return f.RepresentorName, f.RepresentorError
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
