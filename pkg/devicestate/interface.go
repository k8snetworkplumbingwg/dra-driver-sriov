package devicestate

import (
	"context"

	drasriovtypes "github.com/SchSeba/dra-driver-sriov/pkg/types"
)

//go:generate mockgen -destination=mock/mock_devicestate.go -package=mock github.com/SchSeba/dra-driver-sriov/pkg/devicestate DeviceState

// DeviceState defines the minimal interface used by the controller for device state operations.
type DeviceState interface {
	GetAllocatableDevices() drasriovtypes.AllocatableDevices
	UpdateDeviceResourceNames(ctx context.Context, deviceResourceMap map[string]string) error
}

var _ DeviceState = (*Manager)(nil)
