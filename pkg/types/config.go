package types

import (
	"path/filepath"
	"time"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
)

type Flags struct {
	KubeClientConfig flags.KubeClientConfig
	LoggingConfig    *flags.LoggingConfig

	NodeName                      string
	Namespace                     string
	CdiRoot                       string
	KubeletRegistrarDirectoryPath string
	KubeletPluginsDirectoryPath   string
	HealthcheckPort               int
	DefaultInterfacePrefix        string
	DeviceSyncInterval            time.Duration
}

type Config struct {
	Flags         *Flags
	K8sClient     flags.ClientSets
	CancelMainCtx func(error)
}

func (c Config) DriverPluginPath() string {
	return filepath.Join(c.Flags.KubeletPluginsDirectoryPath, consts.DriverName)
}
