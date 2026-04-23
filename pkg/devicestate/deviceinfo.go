package devicestate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/host"
	drasriovtypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

type nadDeviceInfoUtils struct{}

// NewDeviceInfoStore returns the default device-info store implementation backed by NAD utilities.
func NewDeviceInfoStore() DeviceInfoStore {
	return nadDeviceInfoUtils{}
}

// CleanDeviceInfoForDP delegates DP device-info cleanup to NAD utility helpers.
func (nadDeviceInfoUtils) CleanDeviceInfoForDP(resourceName, deviceID string) error {
	return nadutils.CleanDeviceInfoForDP(resourceName, deviceID)
}

// SaveDeviceInfoForDP delegates DP device-info persistence to NAD utility helpers.
func (nadDeviceInfoUtils) SaveDeviceInfoForDP(resourceName, deviceID string, devInfo *nettypes.DeviceInfo) error {
	return nadutils.SaveDeviceInfoForDP(resourceName, deviceID, devInfo)
}

// extractMultusDeviceInfoAttrs resolves the Multus resourceName/deviceID tuple and reports eligibility.
func extractMultusDeviceInfoAttrs(logger klog.Logger, attributes map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) (string, string, bool) {
	resourceName, hasResourceName := getStringDeviceAttribute(logger, attributes, consts.AttributeMultusResourceName)
	deviceID, hasDeviceID := getStringDeviceAttribute(logger, attributes, consts.AttributeMultusDeviceID)

	if !hasResourceName || !hasDeviceID {
		logger.V(2).Info("Device-info eligibility check: one or more required Multus attributes are missing or invalid, therefore DP-compatible device-info generation will be skipped for this allocation result",
			"hasResourceName", hasResourceName,
			"hasDeviceID", hasDeviceID)
		return "", "", false
	}

	logger.V(3).Info("Device-info eligibility check: all required Multus attributes are present and valid, therefore DP-compatible device-info generation is enabled for this allocation result",
		"resourceName", resourceName,
		"deviceID", deviceID)
	return resourceName, deviceID, true
}

// getStringDeviceAttribute reads a required string attribute and logs once when unavailable.
func getStringDeviceAttribute(logger klog.Logger, attributes map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, key resourceapi.QualifiedName) (string, bool) {
	attr, exists := attributes[key]
	if !exists || attr.StringValue == nil || *attr.StringValue == "" {
		logger.V(2).Info("Device-info attribute check: unable to find required attribute",
			"attribute", key)
		return "", false
	}
	return *attr.StringValue, true
}

// syncDeviceInfoFilesForPreparedDevices writes DP-compatible device-info for all eligible prepared devices.
func (s *Manager) syncDeviceInfoFilesForPreparedDevices(ctx context.Context, preparedDevices drasriovtypes.PreparedDevices) error {
	logger := klog.FromContext(ctx).WithName("syncDeviceInfoFilesForPreparedDevices")
	var errs []error

	for _, preparedDevice := range preparedDevices {
		if preparedDevice == nil {
			errs = append(errs, fmt.Errorf("prepared device is nil"))
			continue
		}

		if preparedDevice.MultusResourceName == "" || preparedDevice.MultusDeviceID == "" {
			logger.V(2).Info("Skipping device-info file write: missing required Multus attributes",
				"deviceName", preparedDevice.Device.DeviceName,
				"resourceName", preparedDevice.MultusResourceName,
				"deviceID", preparedDevice.MultusDeviceID)
			continue
		}

		if err := s.saveDeviceInfoForPreparedDevice(preparedDevice); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// saveDeviceInfoForPreparedDevice saves one prepared device as NAD DeviceInfo in DP file layout.
func (s *Manager) saveDeviceInfoForPreparedDevice(preparedDevice *drasriovtypes.PreparedDevice) error {
	if preparedDevice.PciAddress == "" {
		return fmt.Errorf("failed to save device-info for device %q: PCI address is empty", preparedDevice.Device.DeviceName)
	}

	devInfo := &nettypes.DeviceInfo{
		Type:    nettypes.DeviceInfoTypePCI,
		Version: nettypes.DeviceInfoVersion,
		Pci: &nettypes.PciDevice{
			PciAddress: preparedDevice.PciAddress,
		},
	}

	rdmaDevices := host.GetHelpers().GetRDMADevicesForPCI(preparedDevice.PciAddress)
	if len(rdmaDevices) > 0 {
		devInfo.Pci.RdmaDevice = strings.Join(rdmaDevices, ",")
	}

	if err := s.getDeviceInfoStore().CleanDeviceInfoForDP(preparedDevice.MultusResourceName, preparedDevice.MultusDeviceID); err != nil {
		return fmt.Errorf("failed to clean stale device-info for device %q (resourceName=%q, deviceID=%q): %w",
			preparedDevice.Device.DeviceName, preparedDevice.MultusResourceName, preparedDevice.MultusDeviceID, err)
	}

	if err := s.getDeviceInfoStore().SaveDeviceInfoForDP(preparedDevice.MultusResourceName, preparedDevice.MultusDeviceID, devInfo); err != nil {
		return fmt.Errorf("failed to save device-info for device %q (resourceName=%q, deviceID=%q): %w",
			preparedDevice.Device.DeviceName, preparedDevice.MultusResourceName, preparedDevice.MultusDeviceID, err)
	}

	return nil
}

// cleanDeviceInfoFilesForPreparedDevices removes DP device-info files for all eligible prepared devices.
func (s *Manager) cleanDeviceInfoFilesForPreparedDevices(ctx context.Context, preparedDevices drasriovtypes.PreparedDevices) error {
	logger := klog.FromContext(ctx).WithName("cleanDeviceInfoFilesForPreparedDevices")
	var errs []error

	for _, preparedDevice := range preparedDevices {
		if preparedDevice == nil {
			errs = append(errs, fmt.Errorf("prepared device is nil"))
			continue
		}

		if preparedDevice.MultusResourceName == "" || preparedDevice.MultusDeviceID == "" {
			logger.V(3).Info("Skipping device-info cleanup: missing Multus attributes",
				"deviceName", preparedDevice.Device.DeviceName,
				"resourceName", preparedDevice.MultusResourceName,
				"deviceID", preparedDevice.MultusDeviceID)
			continue
		}

		if err := s.getDeviceInfoStore().CleanDeviceInfoForDP(preparedDevice.MultusResourceName, preparedDevice.MultusDeviceID); err != nil {
			errs = append(errs, fmt.Errorf("failed to clean device-info for device %q (resourceName=%q, deviceID=%q): %w",
				preparedDevice.Device.DeviceName, preparedDevice.MultusResourceName, preparedDevice.MultusDeviceID, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// getDeviceInfoStore returns the configured device-info store implementation with a default fallback.
func (s *Manager) getDeviceInfoStore() DeviceInfoStore {
	if s.deviceInfoStore != nil {
		return s.deviceInfoStore
	}
	return NewDeviceInfoStore()
}

// syncDeviceInfoFilesForPreparedDevicesIfNeeded writes device-info files only when MULTUS mode is enabled.
func (s *Manager) syncDeviceInfoFilesForPreparedDevicesIfNeeded(ctx context.Context, preparedDevices drasriovtypes.PreparedDevices) error {
	if !s.isMultusMode() {
		klog.FromContext(ctx).V(4).Info("Skipping device-info file write because configuration mode is not MULTUS",
			"configurationMode", s.configurationMode)
		return nil
	}
	return s.syncDeviceInfoFilesForPreparedDevices(ctx, preparedDevices)
}

// cleanDeviceInfoFilesForPreparedDevicesIfNeeded removes device-info files only when MULTUS mode is enabled.
func (s *Manager) cleanDeviceInfoFilesForPreparedDevicesIfNeeded(ctx context.Context, preparedDevices drasriovtypes.PreparedDevices) error {
	if !s.isMultusMode() {
		klog.FromContext(ctx).V(4).Info("Skipping device-info file cleanup because configuration mode is not MULTUS",
			"configurationMode", s.configurationMode)
		return nil
	}
	return s.cleanDeviceInfoFilesForPreparedDevices(ctx, preparedDevices)
}
