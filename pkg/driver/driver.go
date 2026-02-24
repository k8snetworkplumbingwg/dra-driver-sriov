/*
 * Copyright 2023 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package driver

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path"
	"reflect"
	"time"

	resourceapi "k8s.io/api/resource/v1"
	coreclientset "k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	sriovdratype "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// PolicyReconciler defines the interface for policy reconciliation
type PolicyReconciler interface {
	Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
}

type Driver struct {
	client             coreclientset.Interface
	helper             *kubeletplugin.Helper
	deviceStateManager *devicestate.Manager
	podManager         *podmanager.PodManager
	healthcheck        *Healthcheck
	cancelCtx          func(error)
	config             *sriovdratype.Config
	cdi                *cdi.Handler
	syncInterval       time.Duration
	syncTicker         *time.Ticker
	policyController   PolicyReconciler
}

// Start creates a new DRA driver and starts the kubelet plugin and the healthcheck service after publishing
// the available resources
func Start(ctx context.Context, config *sriovdratype.Config, deviceStateManager *devicestate.Manager, podManager *podmanager.PodManager, cdi *cdi.Handler, syncInterval time.Duration) (*Driver, error) {
	driver := &Driver{
		client:             config.K8sClient.Interface,
		cancelCtx:          config.CancelMainCtx,
		config:             config,
		deviceStateManager: deviceStateManager,
		podManager:         podManager,
		cdi:                cdi,
		syncInterval:       syncInterval,
	}

	helper, err := kubeletplugin.Start(
		ctx,
		driver,
		kubeletplugin.KubeClient(config.K8sClient.Interface),
		kubeletplugin.NodeName(config.Flags.NodeName),
		kubeletplugin.DriverName(consts.DriverName),
		kubeletplugin.RegistrarDirectoryPath(config.Flags.KubeletRegistrarDirectoryPath),
		kubeletplugin.PluginDataDirectoryPath(config.DriverPluginPath()),
	)
	if err != nil {
		klog.FromContext(ctx).Error(err, "Failed to start DRA kubelet plugin")
		return nil, err
	}
	driver.helper = helper

	driver.healthcheck, err = startHealthcheck(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("start healthcheck: %w", err)
	}

	// Publish resources
	if err = driver.PublishResources(ctx); err != nil {
		return nil, fmt.Errorf("failed to publish resources: %w", err)
	}
	return driver, nil
}

// Shutdown shuts down the driver
func (d *Driver) Shutdown(logger klog.Logger) error {
	if d.syncTicker != nil {
		logger.Info("Stopping periodic sync ticker")
		d.syncTicker.Stop()
	}

	if d.healthcheck != nil {
		d.healthcheck.Stop(logger)
	}
	d.helper.Stop()

	// remove the socket files
	// TODO: this is not needed after https://github.com/kubernetes/kubernetes/pull/133934 is merged
	err := os.Remove(path.Join(d.config.Flags.KubeletRegistrarDirectoryPath, consts.DriverName+"-reg.sock"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error removing socket file: %w", err)
	}
	err = os.Remove(path.Join(d.config.DriverPluginPath(), "dra.sock"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error removing socket file: %w", err)
	}

	return nil
}

// PublishResources publishes the devices to the DRA resoruce slice
func (d *Driver) PublishResources(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("PublishResources")
	advertisableDevices := d.deviceStateManager.GetAdvertisableDevices()
	logger.Info("Publishing resources", "advertisableDeviceCount", len(advertisableDevices))

	devices := make([]resourceapi.Device, 0, len(advertisableDevices))
	for device := range maps.Values(advertisableDevices) {
		devices = append(devices, device)
	}
	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			d.config.Flags.NodeName: {
				Slices: []resourceslice.Slice{
					{
						Devices: devices,
					},
				},
			},
		},
	}

	if err := d.helper.PublishResources(ctx, resources); err != nil {
		return err
	}
	return nil
}

// SetPolicyController sets the policy controller reference for reapplying policies during sync
func (d *Driver) SetPolicyController(controller PolicyReconciler) {
	d.policyController = controller
}

// StartPeriodicSync starts the periodic device sync goroutine
func (d *Driver) StartPeriodicSync(ctx context.Context) {
	if d.syncInterval <= 0 {
		klog.FromContext(ctx).Info("Periodic sync disabled", "syncInterval", d.syncInterval)
		return
	}

	klog.FromContext(ctx).Info("Starting periodic device sync", "syncInterval", d.syncInterval)
	d.syncTicker = time.NewTicker(d.syncInterval)
	go d.periodicSyncLoop(ctx)
}

// periodicSyncLoop runs the periodic sync in a goroutine
func (d *Driver) periodicSyncLoop(ctx context.Context) {
	logger := klog.FromContext(ctx).WithName("PeriodicSync")
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping periodic sync")
			if d.syncTicker != nil {
				d.syncTicker.Stop()
			}
			return
		case <-d.syncTicker.C:
			logger.V(2).Info("Running periodic device sync")
			if err := d.syncDevicesAndResources(ctx); err != nil {
				logger.Error(err, "Periodic sync failed")
			}
		}
	}
}

// syncDevicesAndResources re-discovers devices and republishes if changes are detected
func (d *Driver) syncDevicesAndResources(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("SyncDevicesAndResources")

	// 1. Re-discover devices
	logger.V(3).Info("Re-discovering SR-IOV devices")
	newDevices, err := devicestate.DiscoverSriovDevices()
	if err != nil {
		return fmt.Errorf("device discovery failed: %w", err)
	}

	// 2. Compare with current state
	currentDevices := d.deviceStateManager.GetAllocatableDevices()
	changed := deviceSetsAreDifferent(currentDevices, newDevices)

	// 3. If devices changed, update and republish
	if changed {
		logger.Info("Periodic sync detected device changes, updating ResourceSlice",
			"previousDeviceCount", len(currentDevices),
			"newDeviceCount", len(newDevices))

		// Update device state
		d.deviceStateManager.SetAllocatableDevices(newDevices)

		// Re-apply policies to new device set
		if err := d.reapplyPolicies(ctx); err != nil {
			return fmt.Errorf("failed to reapply policies: %w", err)
		}

		// Publish updated resources
		return d.PublishResources(ctx)
	}

	logger.V(3).Info("Periodic sync complete, no changes detected")
	return nil
}

// deviceSetsAreDifferent compares two device maps to detect changes
func deviceSetsAreDifferent(current, new map[string]resourceapi.Device) bool {
	// Check if device count changed
	if len(current) != len(new) {
		return true
	}

	// Check for missing devices or attribute changes
	for name, currentDevice := range current {
		newDevice, exists := new[name]
		if !exists {
			return true
		}

		// Compare device attributes using deep equality
		// This catches changes in NUMA node, capacity, attributes, etc.
		if !reflect.DeepEqual(currentDevice, newDevice) {
			return true
		}
	}

	return false
}

// reapplyPolicies triggers policy reconciliation to update device resourceName attributes
func (d *Driver) reapplyPolicies(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("ReapplyPolicies")

	if d.policyController == nil {
		logger.Error(nil, "Policy controller reference not available - new devices cannot be advertised")
		return fmt.Errorf("policy controller reference required for reapplying policies")
	}

	logger.V(2).Info("Triggering policy reconciliation for updated device set")

	// Trigger a reconciliation by using a synthetic request
	// The controller will re-evaluate all policies against the updated device set
	_, err := d.policyController.Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: d.config.Flags.Namespace,
			Name:      "periodic-sync-trigger",
		},
	})

	if err != nil {
		return fmt.Errorf("policy reconciliation failed: %w", err)
	}

	logger.V(2).Info("Policy reconciliation triggered successfully")
	return nil
}
