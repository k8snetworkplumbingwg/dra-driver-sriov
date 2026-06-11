package types

import (
	"context"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

// MetadataUpdater updates kubelet plugin request metadata for a claim request.
type MetadataUpdater interface {
	UpdateRequestMetadata(
		ctx context.Context,
		claimNamespace, claimName string,
		claimUID k8stypes.UID,
		requestName string,
		devices []kubeletplugin.Device,
	) error
}
