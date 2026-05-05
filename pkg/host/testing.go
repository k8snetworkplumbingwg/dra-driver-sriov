package host

import (
	"context"
	"fmt"
	"os"
	"path"

	"k8s.io/klog/v2"
)

// NewHostForTest creates a Host with injectable providers, for use in unit tests.
// Pass nil to use the default production implementation.
func NewHostForTest(netlinkProvider NetlinkProvider) Interface {
	if netlinkProvider == nil {
		netlinkProvider = &defaultNetlinkProvider{}
	}
	return &Host{
		log:             klog.FromContext(context.Background()).WithName("Host"),
		rdmaProvider:    newRdmaProvider(),
		netlinkProvider: netlinkProvider,
	}
}

// FakeNetlinkProvider is a configurable NetlinkProvider for use in unit tests.
type FakeNetlinkProvider struct {
	EswitchMode  string
	EswitchError error

	// IsPhysicalPort / IsPhysicalPortError control IsDevlinkPhysicalPort.
	// Set IsPhysicalPortError to a non-nil error to simulate devlink being
	// unavailable (the sysfs result will be trusted without validation).
	IsPhysicalPort      bool
	IsPhysicalPortError error
}

func (f *FakeNetlinkProvider) GetDevLinkDeviceEswitchMode(_ string) (string, error) {
	return f.EswitchMode, f.EswitchError
}

func (f *FakeNetlinkProvider) IsDevlinkPhysicalPort(_ string) (bool, error) {
	return f.IsPhysicalPort, f.IsPhysicalPortError
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
