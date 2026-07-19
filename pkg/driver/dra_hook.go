package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
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

	var podUID k8stypes.UID
	for _, claim := range claims {
		if claim != nil && len(claim.Status.ReservedFor) > 0 {
			podUID = claim.Status.ReservedFor[0].UID
			break
		}
	}
	if podUID == "" {
		return result, fmt.Errorf("no pod info found for prepared claims")
	}

	preparedDevices, exists := d.podManager.GetDevicesByPodUID(podUID)
	if !exists && len(claims) > 0 {
		logger.Error(fmt.Errorf("no prepared devices found for pod %s", podUID), "Error preparing devices for claim")
		return result, fmt.Errorf("no prepared devices found for pod %s", podUID)
	}
	// create a global spec file for the pod level environment variables
	pciAddresses := []string{}
	for _, preparedDevice := range preparedDevices {
		device, exist := d.deviceStateManager.GetAllocatableDeviceByName(preparedDevice.Device.DeviceName)
		if !exist {
			baseErr := fmt.Errorf("device not found for device name %s", preparedDevice.Device.DeviceName)
			logger.Error(baseErr, "Error preparing devices for claim")
			if cleanupErr := d.rollbackPreparedClaims(ctx, claims); cleanupErr != nil {
				return result, errors.Join(baseErr, fmt.Errorf("cleanup failed after prepare error: %w", cleanupErr))
			}
			return result, baseErr
		}
		pciAddresses = append(pciAddresses, *device.Attributes[consts.AttributePciAddress].StringValue)
	}

	err := d.cdi.CreateGlobalPodSpecFile(string(podUID), pciAddresses)
	if err != nil {
		logger.Error(err, "Error creating global spec file for pod", "pod", podUID)
		baseErr := fmt.Errorf("error creating global spec file for pod: %w", err)
		if cleanupErr := d.rollbackPreparedClaims(ctx, claims); cleanupErr != nil {
			return result, errors.Join(baseErr, fmt.Errorf("cleanup failed after global spec error: %w", cleanupErr))
		}
		return result, baseErr
	}

	logger.V(3).Info("Prepared claims", "result", result)
	return result, nil
}

// rollbackPreparedClaims rolls back successful claim preparations that were stored in pod manager state.
func (d *Driver) rollbackPreparedClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) error {
	var errs []error
	for _, claim := range claims {
		if claim == nil {
			continue
		}
		if err := d.unprepareResourceClaim(ctx, kubeletplugin.NamespacedObject{
			NamespacedName: k8stypes.NamespacedName{
				Name:      claim.Name,
				Namespace: claim.Namespace,
			},
			UID: claim.UID,
		}); err != nil {
			errs = append(errs, fmt.Errorf("failed to rollback claim %s: %w", claim.UID, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
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
			prepared = append(prepared, preparedDevice.ToKubeletPluginDevice(nil))
		}
		return kubeletplugin.PrepareResult{Devices: prepared}
	}

	// Resolve MAC addresses for this specific claim.
	macAddresses, err := d.getMACAddressesForClaim(ctx, claim)
	if err != nil {
		logger.Error(err, "Failed to get MAC addresses from pod annotations")
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("failed to get MAC addresses from pod annotations: %w", err),
		}
	}

	// if the pod claim is not prepared, prepare the devices for the claim
	preparedDevices, err = d.deviceStateManager.PrepareDevicesForClaim(ctx, ifNameIndex, claim, macAddresses)
	if err != nil {
		logger.Error(err, "Error preparing devices for claim", "claim", claim.UID)
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("error preparing devices for claim %v: %w", claim.UID, err),
		}
	}

	var prepared []kubeletplugin.Device
	for _, preparedDevice := range preparedDevices {
		prepared = append(prepared, preparedDevice.ToKubeletPluginDevice(nil))
	}

	err = d.podManager.Set(podUID, claim.UID, preparedDevices)
	if err != nil {
		logger.Error(err, "Error setting prepared devices for pod into pod manager", "pod", podUID)
		if cleanupErr := d.deviceStateManager.Unprepare(string(claim.UID), preparedDevices); cleanupErr != nil {
			return kubeletplugin.PrepareResult{
				Err: fmt.Errorf("error setting prepared devices for pod %s into pod manager: %w; cleanup failed: %v", podUID, err, cleanupErr),
			}
		}
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

// getMACAddressesForClaim resolves per-request MACs for the given claim.
// KubeVirt stores MACs on the launcher pod as "<podClaimName>/<requestName>" keys.
func (d *Driver) getMACAddressesForClaim(ctx context.Context, claim *resourceapi.ResourceClaim) (map[string]string, error) {
	logger := klog.FromContext(ctx).WithName("getMACAddressesForClaim")

	if len(claim.Status.ReservedFor) == 0 {
		return nil, fmt.Errorf("no pod info in claim")
	}

	podRef := claim.Status.ReservedFor[0]
	// ResourceClaimConsumerReference only has Name and UID, we need to get namespace from claim
	pod, err := d.client.CoreV1().Pods(claim.Namespace).Get(ctx, podRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %s/%s: %w", claim.Namespace, podRef.Name, err)
	}

	macMap := make(map[string]string)
	if pod.Annotations == nil {
		return macMap, nil
	}

	macJSON := pod.Annotations[types.DRANetworkMACsAnnotation]
	if macJSON == "" {
		logger.V(3).Info("No DRA network MAC annotation found on pod")
		return macMap, nil
	}

	if err := json.Unmarshal([]byte(macJSON), &macMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MAC addresses from annotation: %w", err)
	}
	if len(macMap) == 0 {
		return map[string]string{}, nil
	}

	podClaimName := resolvePodClaimNameForResourceClaim(pod, claim.Name)
	if podClaimName == "" {
		return nil, fmt.Errorf("failed to resolve pod claim name for resource claim %q while %q is set", claim.Name, types.DRANetworkMACsAnnotation)
	}

	claimMACs := make(map[string]string)
	macPrefix := podClaimName + "/"
	for key, mac := range macMap {
		if !strings.HasPrefix(key, macPrefix) {
			continue
		}
		requestName := strings.TrimPrefix(key, macPrefix)
		if requestName == "" {
			return nil, fmt.Errorf("invalid MAC key %q: missing request name", key)
		}
		claimMACs[requestName] = mac
	}

	logger.V(2).Info("Resolved MAC addresses for claim", "resourceClaimName", claim.Name, "podClaimName", podClaimName, "macAddressesByRequest", claimMACs)
	return claimMACs, nil
}

func resolvePodClaimNameForResourceClaim(pod *corev1.Pod, resourceClaimName string) string {
	if pod == nil || resourceClaimName == "" {
		return ""
	}

	for _, podClaim := range pod.Spec.ResourceClaims {
		if podClaim.ResourceClaimName != nil && *podClaim.ResourceClaimName == resourceClaimName {
			return podClaim.Name
		}
	}

	for _, claimStatus := range pod.Status.ResourceClaimStatuses {
		if claimStatus.ResourceClaimName != nil && *claimStatus.ResourceClaimName == resourceClaimName {
			return claimStatus.Name
		}
	}

	return ""
}

func (d *Driver) HandleError(ctx context.Context, err error, msg string) {
	utilruntime.HandleErrorWithContext(ctx, err, msg)
	if !errors.Is(err, kubeletplugin.ErrRecoverable) && d.cancelCtx != nil {
		d.cancelCtx(fmt.Errorf("fatal background error: %w", err))
	}
}
