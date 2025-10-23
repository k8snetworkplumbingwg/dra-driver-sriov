package cdi

import (
	"fmt"
	"os"
	"strings"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

const (
	cdiVendor = consts.DriverName
	cdiClass  = "vf"
	cdiKind   = cdiVendor + "/" + cdiClass

	cdiCommonDeviceName = "dra-driver-sriov"
)

type Handler struct {
	cache *cdiapi.Cache
}

func NewHandler(cdiRootPath string) (*Handler, error) {
	cache, err := cdiapi.NewCache(
		cdiapi.WithSpecDirs(cdiRootPath),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create a new CDI cache: %w", err)
	}
	handler := &Handler{
		cache: cache,
	}

	return handler, nil
}

// NOT used right now
func (cdi *Handler) CreateCommonSpecFile() error {
	spec := &cdispec.Spec{
		Kind: cdiKind,
		Devices: []cdispec.Device{
			{
				Name: cdiCommonDeviceName,
				ContainerEdits: cdispec.ContainerEdits{
					Env: []string{
						fmt.Sprintf("KUBERNETES_NODE_NAME=%s", os.Getenv("NODE_NAME")),
						fmt.Sprintf("DRA_RESOURCE_DRIVER_NAME=%s", consts.DriverName),
					},
				},
			},
		},
	}

	minVersion, err := cdiapi.MinimumRequiredVersion(spec)
	if err != nil {
		return fmt.Errorf("failed to get minimum required CDI spec version: %v", err)
	}
	spec.Version = minVersion

	specName, err := cdiapi.GenerateNameForTransientSpec(spec, cdiCommonDeviceName)
	if err != nil {
		return fmt.Errorf("failed to generate Spec name: %w", err)
	}

	return cdi.cache.WriteSpec(spec, specName)
}

func (cdi *Handler) CreateClaimSpecFile(preparedDevices types.PreparedDevices) error {
	claimUID := string(preparedDevices[0].ClaimNamespacedName.UID)
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)

	spec := &cdispec.Spec{
		Kind:    cdiKind,
		Devices: []cdispec.Device{},
	}

	for _, device := range preparedDevices {
		cdiDevice := cdispec.Device{
			Name:           fmt.Sprintf("%s-%s", claimUID, device.Device.DeviceName),
			ContainerEdits: *device.ContainerEdits.ContainerEdits,
		}

		spec.Devices = append(spec.Devices, cdiDevice)
	}
	minVersion, err := cdiapi.MinimumRequiredVersion(spec)
	if err != nil {
		return fmt.Errorf("failed to get minimum required CDI spec version: %v", err)
	}
	spec.Version = minVersion

	return cdi.cache.WriteSpec(spec, specName)
}

func (cdi *Handler) CreateGlobalPodSpecFile(podUID string, pciAddresses []string) error {
	envs := []string{fmt.Sprintf("SRIOVNETWORK_PCI_ADDRESSES=%s", strings.Join(pciAddresses, ","))}
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, podUID)

	cdiDevice := cdispec.Device{
		Name: podUID,
		ContainerEdits: cdispec.ContainerEdits{
			Env: envs,
		},
	}

	spec := &cdispec.Spec{
		Kind:    cdiKind,
		Devices: []cdispec.Device{cdiDevice},
	}

	minVersion, err := cdiapi.MinimumRequiredVersion(spec)
	if err != nil {
		return fmt.Errorf("failed to get minimum required CDI spec version: %v", err)
	}
	spec.Version = minVersion

	return cdi.cache.WriteSpec(spec, specName)
}

func (cdi *Handler) DeleteSpecFile(uid string) error {
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, uid)
	return cdi.cache.RemoveSpec(specName)
}

func (cdi *Handler) GetClaimDevices(claimUID string, device string) string {
	return cdiparser.QualifiedName(cdiVendor, cdiClass, fmt.Sprintf("%s-%s", claimUID, device))
}

func (cdi *Handler) GetPodSpecName(podUID string) string {
	return cdiparser.QualifiedName(cdiVendor, cdiClass, podUID)
}
