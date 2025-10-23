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

	resourceapi "k8s.io/api/resource/v1"
	coreclientset "k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	sriovdratype "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

type Driver struct {
	client             coreclientset.Interface
	helper             *kubeletplugin.Helper
	deviceStateManager *devicestate.Manager
	podManager         *podmanager.PodManager
	healthcheck        *Healthcheck
	cancelCtx          func(error)
	config             *sriovdratype.Config
	cdi                *cdi.Handler
}

// Start creates a new DRA driver and starts the kubelet plugin and the healthcheck service after publishing
// the available resources
func Start(ctx context.Context, config *sriovdratype.Config, deviceStateManager *devicestate.Manager, podManager *podmanager.PodManager, cdi *cdi.Handler) (*Driver, error) {
	driver := &Driver{
		client:             config.K8sClient.Interface,
		cancelCtx:          config.CancelMainCtx,
		config:             config,
		deviceStateManager: deviceStateManager,
		podManager:         podManager,
		cdi:                cdi,
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
	devices := make([]resourceapi.Device, 0, len(d.deviceStateManager.GetAllocatableDevices()))
	for device := range maps.Values(d.deviceStateManager.GetAllocatableDevices()) {
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
