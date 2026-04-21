package devicestate

import (
	"context"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	resourceapi "k8s.io/api/resource/v1"

	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

//go:generate mockgen -destination=mock/mock_devicestate.go -package=mock github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate DeviceState
//go:generate mockgen -destination=mock/mock_deviceinfostore.go -package=mock github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate DeviceInfoStore

// DeviceState defines the minimal interface used by the controller for device state operations.
type DeviceState interface {
	// GetAllocatableDevices returns the full discovered device set.
	GetAllocatableDevices() drasriovtypes.AllocatableDevices
	// UpdatePolicyDevices updates the set of advertised devices and their policy-applied attributes.
	// Keys in policyDevices are device names matched by policies (these will be advertised).
	// Values are additional attributes from resolved DeviceAttributes objects.
	// Devices not in the map are excluded from advertisement, and their policy-set attributes are cleared.
	UpdatePolicyDevices(ctx context.Context, policyDevices map[string]map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) error
}

// DeviceInfoStore abstracts DP device-info persistence and cleanup.
type DeviceInfoStore interface {
	// CleanDeviceInfoForDP removes persisted device-info for a specific DP resource/device tuple.
	CleanDeviceInfoForDP(resourceName, deviceID string) error
	// SaveDeviceInfoForDP persists device-info for a specific DP resource/device tuple.
	SaveDeviceInfoForDP(resourceName, deviceID string, devInfo *nettypes.DeviceInfo) error
}

var _ DeviceState = (*Manager)(nil)
