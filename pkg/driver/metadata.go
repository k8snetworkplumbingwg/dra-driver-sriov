package driver

import (
	"context"
	"fmt"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

// UpdateRequestMetadata refreshes per-request metadata files for a prepared claim.
func (d *Driver) UpdateRequestMetadata(
	ctx context.Context,
	claimNamespace, claimName string,
	claimUID k8stypes.UID,
	requestName string,
	devices []kubeletplugin.Device,
) error {
	if d.helper == nil {
		return fmt.Errorf("kubelet plugin helper is not initialized")
	}
	return d.helper.UpdateRequestMetadata(ctx, claimNamespace, claimName, claimUID, requestName, devices)
}
