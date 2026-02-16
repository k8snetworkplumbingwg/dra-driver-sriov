package devicestate

import (
	"context"

	resourceapi "k8s.io/api/resource/v1"

	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

//go:generate mockgen -destination=mock/mock_devicestate.go -package=mock github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate DeviceState

// DeviceState defines the minimal interface used by the controller for device state operations.
type DeviceState interface {
	GetAllocatableDevices() drasriovtypes.AllocatableDevices
	// GetAdvertisedDevices returns only devices that are matched by a policy
	// and should be published in the ResourceSlice.
	GetAdvertisedDevices() drasriovtypes.AllocatableDevices
	// UpdatePolicyDevices updates the set of advertised devices and their policy-applied attributes.
	// Keys in policyDevices are device names matched by policies (these will be advertised).
	// Values are additional attributes from resolved DeviceAttributes objects.
	// Devices not in the map are excluded from advertisement, and their policy-set attributes are cleared.
	UpdatePolicyDevices(ctx context.Context, policyDevices map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) error
}

var _ DeviceState = (*Manager)(nil)
