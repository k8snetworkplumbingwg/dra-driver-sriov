package driver

import (
	"context"
	"errors"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
)

func (d *Driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[k8stypes.UID]kubeletplugin.PrepareResult, error) {
	result := make(map[k8stypes.UID]kubeletplugin.PrepareResult)
	if len(claims) == 0 {
		return result, nil
	}
	logger := klog.FromContext(ctx).WithName("PrepareResourceClaims")
	logger.V(3).Info("claims", "claims", claims)

	// we share this between all the claims so we can enumerate network interfaces
	ifNameIndex := 0
	// let's prepare the claims
	for _, claim := range claims {
		logger.V(1).Info("Preparing claim", "claim", claim.UID)
		logger.V(3).Info("Claim", "claim", claim)
		result[claim.UID] = d.prepareResourceClaim(ctx, &ifNameIndex, claim)
		logger.V(1).Info("Prepared claim", "claim", claim.UID, "result", result[claim.UID])
		if result[claim.UID].Err != nil {
			logger.Error(result[claim.UID].Err, "failed to prepare resource claim", "claim", claim)
		}
	}

	preparedDevices, exists := d.podManager.GetDevicesByPodUID(claims[0].Status.ReservedFor[0].UID)
	if !exists && len(claims) > 0 {
		logger.Error(fmt.Errorf("no prepared devices found for pod %s", claims[0].Status.ReservedFor[0].UID), "Error preparing devices for claim")
		return result, fmt.Errorf("no prepared devices found for pod %s", claims[0].Status.ReservedFor[0].UID)
	}
	// create a global spec file for the pod level environment variables
	pciAddresses := []string{}
	for _, preparedDevice := range preparedDevices {
		device, exist := d.deviceStateManager.GetAllocatedDeviceByDeviceName(preparedDevice.Device.DeviceName)
		if !exist {
			logger.Error(fmt.Errorf("device not found for device name %s", preparedDevice.Device.DeviceName), "Error preparing devices for claim")
			return result, fmt.Errorf("device not found for device name %s", preparedDevice.Device.DeviceName)
		}
		pciAddresses = append(pciAddresses, *device.Attributes[consts.AttributePciAddress].StringValue)
	}

	err := d.cdi.CreateGlobalPodSpecFile(string(claims[0].Status.ReservedFor[0].UID), pciAddresses)
	if err != nil {
		logger.Error(err, "Error creating global spec file for pod", "pod", claims[0].Status.ReservedFor[0].UID)
		return result, fmt.Errorf("error creating global spec file for pod: %w", err)
	}

	logger.V(3).Info("Prepared claims", "result", result)
	return result, nil
}

func (d *Driver) prepareResourceClaim(ctx context.Context, ifNameIndex *int, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	logger := klog.FromContext(ctx).WithName("prepareResourceClaim")

	// Get pod info from claim
	if len(claim.Status.ReservedFor) == 0 {
		logger.Error(fmt.Errorf("no pod info found for claim %s/%s/%s", claim.Namespace, claim.Name, claim.UID), "Error preparing devices for claim")
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("no pod info found for claim %s/%s/%s", claim.Namespace, claim.Name, claim.UID),
		}
	} else if len(claim.Status.ReservedFor) > 1 {
		logger.Error(fmt.Errorf("multiple pods found for claim %s/%s/%s not supported", claim.Namespace, claim.Name, claim.UID), "Error preparing devices for claim")
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("multiple pods found for claim %s/%s/%s not supported", claim.Namespace, claim.Name, claim.UID),
		}
	}

	if claim.Status.Allocation == nil {
		logger.Error(fmt.Errorf("claim not yet allocated"), "Prepare failed", "claim", claim.UID)
		return kubeletplugin.PrepareResult{Err: fmt.Errorf("claim not yet allocated")}
	}

	// get the pod UID
	podUID := claim.Status.ReservedFor[0].UID

	// check if the pod claim is already prepared and return the prepared devices
	preparedDevices, isAlreadyPrepared := d.podManager.Get(podUID, claim.UID)
	if isAlreadyPrepared {
		var prepared []kubeletplugin.Device
		for _, preparedDevice := range preparedDevices {
			prepared = append(prepared, kubeletplugin.Device{
				Requests:     preparedDevice.Device.GetRequestNames(),
				PoolName:     preparedDevice.Device.GetPoolName(),
				DeviceName:   preparedDevice.Device.GetDeviceName(),
				CDIDeviceIDs: preparedDevice.Device.GetCDIDeviceIDs(),
			})
		}
		return kubeletplugin.PrepareResult{Devices: prepared}
	}

	// if the pod claim is not prepared, prepare the devices for the claim
	preparedDevices, err := d.deviceStateManager.PrepareDevicesForClaim(ctx, ifNameIndex, claim)
	if err != nil {
		logger.Error(err, "Error preparing devices for claim", "claim", claim.UID)
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("error preparing devices for claim %v: %w", claim.UID, err),
		}
	}

	var prepared []kubeletplugin.Device
	for _, preparedDevice := range preparedDevices {
		prepared = append(prepared, kubeletplugin.Device{
			Requests:     preparedDevice.Device.GetRequestNames(),
			PoolName:     preparedDevice.Device.GetPoolName(),
			DeviceName:   preparedDevice.Device.GetDeviceName(),
			CDIDeviceIDs: preparedDevice.Device.GetCDIDeviceIDs(),
		})
	}

	err = d.podManager.Set(podUID, claim.UID, preparedDevices)
	if err != nil {
		logger.Error(err, "Error setting prepared devices for pod into pod manager", "pod", podUID)
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("error setting prepared devices for pod %s into pod manager: %w", podUID, err),
		}
	}

	// Store original devices list to preserve across conflict retries
	originalDevices := claim.Status.Devices

	err = wait.ExponentialBackoffWithContext(ctx, consts.Backoff, func(ctx context.Context) (bool, error) {
		_, updateErr := d.client.ResourceV1().ResourceClaims(claim.Namespace).UpdateStatus(ctx, claim, metav1.UpdateOptions{})
		if updateErr != nil {
			// If this is a conflict error, fetch fresh claim and copy over devices list
			if apierrors.IsConflict(updateErr) {
				logger.V(2).Info("Conflict detected, refreshing claim", "claim", claim.UID)

				freshClaim, fetchErr := d.client.ResourceV1().ResourceClaims(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
				if fetchErr != nil {
					logger.V(2).Info("Failed to fetch fresh claim", "claim", claim.UID, "error", fetchErr.Error())
					return false, nil // Continue retrying
				}

				// Copy original devices list to fresh claim
				freshClaim.Status.Devices = originalDevices
				claim = freshClaim // Use fresh claim for next retry

				logger.V(2).Info("Refreshed claim, retrying status update", "claim", claim.UID)
			} else {
				logger.V(2).Info("Retrying claim status update", "claim", claim.UID, "error", updateErr.Error())
			}
			return false, nil // Return false to continue retrying, nil to not fail immediately
		}
		return true, nil // Success
	})

	if err != nil {
		logger.Error(err, "Failed to update claim status after retries", "claim", claim.UID)
	}

	logger.V(3).Info("Returning prepared devices for claim", "claim", claim.UID, "prepared", prepared)
	return kubeletplugin.PrepareResult{Devices: prepared}
}

func (d *Driver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[k8stypes.UID]error, error) {
	logger := klog.FromContext(ctx).WithName("UnprepareResourceClaims")
	logger.V(1).Info("UnprepareResourceClaims is called", "number of claims", len(claims))
	logger.V(3).Info("claims", "claims", claims)
	result := make(map[k8stypes.UID]error)

	for _, claim := range claims {
		result[claim.UID] = d.unprepareResourceClaim(ctx, claim)
	}

	logger.V(3).Info("Unprepared claims", "result", result)
	return result, nil
}

func (d *Driver) unprepareResourceClaim(ctx context.Context, claim kubeletplugin.NamespacedObject) error {
	logger := klog.FromContext(ctx).WithName("unprepareResourceClaim")
	logger.V(1).Info("Unpreparing resource claim", "claim", claim.UID)
	logger.V(3).Info("claim", "claim", claim)

	preparedDevices, found := d.podManager.GetByClaim(claim)
	if !found {
		return nil
	}

	if err := d.deviceStateManager.Unprepare(string(claim.UID), preparedDevices); err != nil {
		return fmt.Errorf("error unpreparing devices for claim %v: %w", claim.UID, err)
	}

	// delete the claim from the pod manager
	err := d.podManager.DeleteClaim(claim)
	if err != nil {
		logger.Error(err, "Error deleting claim from pod manager", "claim", claim.UID)
		return fmt.Errorf("error deleting claim %s from pod manager: %w", claim.UID, err)
	}
	return nil
}

func (d *Driver) HandleError(ctx context.Context, err error, msg string) {
	utilruntime.HandleErrorWithContext(ctx, err, msg)
	if !errors.Is(err, kubeletplugin.ErrRecoverable) && d.cancelCtx != nil {
		d.cancelCtx(fmt.Errorf("fatal background error: %w", err))
	}
}
