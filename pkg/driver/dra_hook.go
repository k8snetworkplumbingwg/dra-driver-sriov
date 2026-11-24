package driver

import (
	"context"
	"errors"
	"fmt"
	"sort"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// PrepareResourceClaims validates claims before cache reuse, device preparation, and Pod CDI publication.
func (d *Driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[k8stypes.UID]kubeletplugin.PrepareResult, error) {
	result := make(map[k8stypes.UID]kubeletplugin.PrepareResult)
	if len(claims) == 0 {
		return result, nil
	}
	logger := klog.FromContext(ctx).WithName("PrepareResourceClaims")
	logger.V(3).Info("claims", "count", len(claims))

	// Resolve the whole batch before cache inspection or device operations.
	resolvedClaims := make(map[k8stypes.UID]*devicestate.ResolvedClaimConfiguration, len(claims))
	groups := make(map[k8stypes.UID][]*resourceapi.ResourceClaim)
	validGroups := make(map[k8stypes.UID][]*resourceapi.ResourceClaim)
	for _, claim := range claims {
		if claim == nil {
			return result, fmt.Errorf("nil ResourceClaim")
		}
		if len(claim.Status.ReservedFor) == 1 {
			podUID := claim.Status.ReservedFor[0].UID
			groups[podUID] = append(groups[podUID], claim)
		}
		resolved, err := d.resolveResourceClaimConfiguration(claim)
		if err != nil {
			logger.Error(err, "failed to resolve resource claim configuration", "claim", claim.UID)
			result[claim.UID] = kubeletplugin.PrepareResult{Err: err}
			continue
		}
		resolvedClaims[claim.UID] = resolved
		podUID := claim.Status.ReservedFor[0].UID
		validGroups[podUID] = append(validGroups[podUID], claim)
	}
	// Reject Pod-wide fixed-name conflicts before assigning names or changing hardware.
	newlyPrepared := make(map[k8stypes.UID]bool)
	for podUID, validClaims := range validGroups {
		prepared := d.podManager.GetClaimsByPodUID(podUID)
		err := reservePodInterfaceNames(podUID, validClaims, resolvedClaims, prepared)
		for _, claim := range validClaims {
			if err != nil {
				result[claim.UID] = kubeletplugin.PrepareResult{Err: err}
				continue
			}
			_, cached := prepared[claim.UID]
			newlyPrepared[claim.UID] = !cached
		}
	}
	ifNameIndex := 0
	for _, claim := range claims {
		if result[claim.UID].Err != nil {
			continue
		}
		logger.V(1).Info("Preparing claim", "claim", claim.UID)
		result[claim.UID] = d.prepareResourceClaim(ctx, &ifNameIndex, claim, resolvedClaims[claim.UID])
		logger.V(1).Info("Prepared claim", "claim", claim.UID, "result", result[claim.UID])
		if result[claim.UID].Err != nil {
			logger.Error(result[claim.UID].Err, "failed to prepare resource claim", "claim", claim.UID)
		}
	}

	if len(groups) == 0 {
		return result, fmt.Errorf("no pod info found for prepared claims")
	}
	for uid, podClaims := range groups {
		if err := d.publishPodCDI(ctx, uid, podClaims, result, newlyPrepared); err != nil {
			logger.Error(err, "Error publishing global spec file for pod", "pod", uid)
			return result, err
		}
	}
	logger.V(3).Info("Prepared claims", "result", result)
	return result, nil
}

// reservePodInterfaceNames reserves saved and fixed interface names across the Pod's claims.
func reservePodInterfaceNames(podUID k8stypes.UID, claims []*resourceapi.ResourceClaim, resolved map[k8stypes.UID]*devicestate.ResolvedClaimConfiguration, prepared types.PreparedDevicesByClaimID) error {
	owners := make(map[string]k8stypes.UID)
	reserve := func(name string, claimUID k8stypes.UID) error {
		if name == "" {
			return nil
		}
		if owner, exists := owners[name]; exists && owner != claimUID {
			return fmt.Errorf("preflight Pod %s: interface name %q conflicts between claims %s and %s", podUID, name, owner, claimUID)
		}
		owners[name] = claimUID
		return nil
	}
	for claimUID, devices := range prepared {
		for _, device := range devices {
			if device == nil {
				continue
			}
			if err := reserve(device.IfName, claimUID); err != nil {
				return err
			}
		}
	}
	for _, claim := range claims {
		for _, device := range resolved[claim.UID].ByKey {
			if err := reserve(device.Config.IfName, claim.UID); err != nil {
				return err
			}
		}
		// Share one reservation map per Pod; preflight excludes the claim's own names.
		resolved[claim.UID].ExistingIfNames = owners
	}
	return nil
}

// publishPodCDI publishes verified devices and rolls back only new preparations on publication failure.
func (d *Driver) publishPodCDI(ctx context.Context, podUID k8stypes.UID, claims []*resourceapi.ResourceClaim, result map[k8stypes.UID]kubeletplugin.PrepareResult, newlyPrepared map[k8stypes.UID]bool) (err error) {
	// Publish from all currently verified Pod claims, including earlier prepare batches.
	// A rejected record is retained for cleanup and excluded from this publication.
	hasSuccess := false
	successful := make([]*resourceapi.ResourceClaim, 0, len(claims))
	for _, claim := range claims {
		if result[claim.UID].Err == nil {
			hasSuccess = true
			if newlyPrepared[claim.UID] {
				successful = append(successful, claim)
			}
		}
	}
	if !hasSuccess {
		return nil
	}
	defer func() {
		if err != nil {
			if cleanupErr := d.rollbackPreparedClaims(ctx, successful); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("cleanup failed after Pod CDI publication error: %w", cleanupErr))
			}
		}
	}()
	pciAddresses := []string{}
	for uid, devices := range d.podManager.GetClaimsByPodUID(podUID) {
		if res, inBatch := result[uid]; inBatch {
			if res.Err != nil {
				continue
			}
			// Fresh and cached batch successes were already verified by prepareResourceClaim.
		} else {
			if len(devices) == 0 {
				continue
			}
			if d.client == nil {
				return fmt.Errorf("cannot verify Pod claim %s: Kubernetes client unavailable", uid)
			}
			claim, err := d.client.ResourceV1().ResourceClaims(devices[0].ClaimNamespacedName.Namespace).Get(ctx, devices[0].ClaimNamespacedName.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("cannot verify Pod claim %s: %w", uid, err)
			}
			mode, err := devicestate.NormalizeConfigurationMode(d.config.Flags.ConfigurationMode)
			if err != nil {
				return err
			}
			resolved, err := devicestate.ResolveClaimConfiguration(claim, configapi.Decoder, mode)
			if err != nil {
				return fmt.Errorf("cannot verify Pod claim %s: %w", uid, err)
			}
			meta, _ := d.podManager.ClaimProvenance(podUID, uid)
			if err := devicestate.VerifyPreparedClaim(claim, resolved, devices, meta); err != nil {
				return err
			}
		}
		for _, device := range devices {
			pciAddresses = append(pciAddresses, device.PciAddress)
		}
	}
	sort.Strings(pciAddresses)
	if err := d.cdi.CreateGlobalPodSpecFile(string(podUID), pciAddresses); err != nil {
		return fmt.Errorf("error creating global spec file for pod: %w", err)
	}

	return nil
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

// resolveResourceClaimConfiguration checks the Pod reservation and resolves trusted allocation configuration.
func (d *Driver) resolveResourceClaimConfiguration(claim *resourceapi.ResourceClaim) (*devicestate.ResolvedClaimConfiguration, error) {
	if len(claim.Status.ReservedFor) == 0 {
		return nil, fmt.Errorf("no pod info found for claim %s/%s/%s", claim.Namespace, claim.Name, claim.UID)
	}
	if len(claim.Status.ReservedFor) > 1 {
		return nil, fmt.Errorf("multiple pods found for claim %s/%s/%s not supported", claim.Namespace, claim.Name, claim.UID)
	}
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}
	if d.config == nil || d.config.Flags == nil {
		return nil, fmt.Errorf("driver configuration is not initialized")
	}
	mode, err := devicestate.NormalizeConfigurationMode(d.config.Flags.ConfigurationMode)
	if err != nil {
		return nil, fmt.Errorf("configuration mode: %w", err)
	}
	resolved, err := devicestate.ResolveClaimConfiguration(claim, configapi.Decoder, mode)
	if err != nil {
		return nil, fmt.Errorf("configuration policy: %w", err)
	}
	return resolved, nil
}

// prepareResourceClaim verifies cached provenance or prepares and checkpoints a resolved claim.
func (d *Driver) prepareResourceClaim(ctx context.Context, ifNameIndex *int, claim *resourceapi.ResourceClaim, resolved *devicestate.ResolvedClaimConfiguration) kubeletplugin.PrepareResult {
	logger := klog.FromContext(ctx).WithName("prepareResourceClaim")
	var err error
	if resolved == nil {
		resolved, err = d.resolveResourceClaimConfiguration(claim)
		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
	}
	podUID := claim.Status.ReservedFor[0].UID

	preparedDevices, isAlreadyPrepared := d.podManager.Get(podUID, claim.UID)
	if isAlreadyPrepared {
		meta, known := d.podManager.ClaimProvenance(podUID, claim.UID)
		if !known {
			meta.LegacyCleanupOnly = true
		}
		if err := devicestate.VerifyPreparedClaim(claim, resolved, preparedDevices, meta); err != nil {
			return kubeletplugin.PrepareResult{
				Err: err,
			}
		}
		var prepared []kubeletplugin.Device
		for _, preparedDevice := range preparedDevices {
			prepared = append(prepared, preparedDevice.ToKubeletPluginDevice(nil))
		}
		return kubeletplugin.PrepareResult{Devices: prepared}
	}

	// Reject unsafe legacy Pod state before any new host changes. Keep it available for cleanup.
	if resolved.ExistingIfNames == nil {
		resolved.ExistingIfNames = make(map[string]k8stypes.UID)
	}
	for uid, devices := range d.podManager.GetClaimsByPodUID(podUID) {
		meta, _ := d.podManager.ClaimProvenance(podUID, uid)
		if meta.LegacyCleanupOnly || meta.ConfigPolicyVersion != resolved.ConfigPolicyVersion || meta.ConfigFingerprint == "" {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("migration: legacy prepared state blocks new preparation for Pod %s", podUID)}
		}
		for _, device := range devices {
			if device.IfName != "" {
				resolved.ExistingIfNames[device.IfName] = uid
			}
		}
	}
	preparedDevices, err = d.deviceStateManager.PrepareDevicesForClaim(ctx, ifNameIndex, claim, resolved)
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

	err = d.podManager.Set(podUID, claim.UID, preparedDevices, &types.PreparedClaimMeta{
		ConfigPolicyVersion: resolved.ConfigPolicyVersion,
		ConfigFingerprint:   resolved.Fingerprint,
	})
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

	// status.devices is shared with every other driver that contributed a device
	// to the claim, so the retry refetches and merges on conflict rather than
	// restoring a pre-conflict snapshot. The status write is best-effort here: on
	// failure the devices are still prepared, so log and return them.
	if err := types.UpdateClaimStatusWithRetry(
		ctx,
		d.client.ResourceV1().ResourceClaims(claim.Namespace),
		claim,
		consts.DriverName,
		consts.Backoff,
	); err != nil {
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

// unprepareResourceClaim cleans saved state, removing shared Pod CDI only for the last claim.
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

	// Preserve shared Pod CDI while other claims still need it, including rollback.
	// Delete before the checkpoint entry so a failed CDI removal can be retried.
	if len(preparedDevices) > 0 && preparedDevices[0] != nil {
		podUID := k8stypes.UID(preparedDevices[0].PodUID)
		if podUID != "" && len(d.podManager.GetClaimsByPodUID(podUID)) == 1 {
			if err := d.cdi.DeleteSpecFile(string(podUID)); err != nil {
				return fmt.Errorf("error deleting CDI spec file for Pod %s: %w", podUID, err)
			}
		}
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
