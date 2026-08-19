package nri

import (
	"encoding/json"
	"fmt"

	"github.com/containerd/nri/pkg/api"
)

// getNetworkNamespace returns the pod network namespace path when present.
func getNetworkNamespace(pod *api.PodSandbox) string {
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			return namespace.Path
		}
	}

	return ""
}

// injectDeviceIDRuntimeConfig adds runtimeConfig.deviceID to a CNI netconf JSON document.
func injectDeviceIDRuntimeConfig(netAttachDefConfig, deviceID string) (string, error) {
	if netAttachDefConfig == "" || deviceID == "" {
		return netAttachDefConfig, nil
	}

	rawConfig := map[string]interface{}{}
	if err := json.Unmarshal([]byte(netAttachDefConfig), &rawConfig); err != nil {
		return "", fmt.Errorf("failed to unmarshal net attach def config: %w", err)
	}
	if rawConfig == nil {
		return "", fmt.Errorf("net attach def config must be a JSON object")
	}

	pluginsValue, hasPlugins := rawConfig["plugins"]
	if hasPlugins {
		plugins, ok := pluginsValue.([]interface{})
		if !ok {
			return "", fmt.Errorf("plugins must be a JSON array when present")
		}
		if len(plugins) == 0 {
			return "", fmt.Errorf("plugins array cannot be empty")
		}
		for idx, pluginValue := range plugins {
			pluginConfig, ok := pluginValue.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("plugin config at index %d must be a JSON object", idx)
			}
			if err := setRuntimeConfigDeviceID(pluginConfig, deviceID); err != nil {
				return "", fmt.Errorf("failed to set runtimeConfig.deviceID for plugin at index %d: %w", idx, err)
			}
		}
	} else {
		if err := setRuntimeConfigDeviceID(rawConfig, deviceID); err != nil {
			return "", err
		}
	}

	modifiedConfig, err := json.Marshal(rawConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal net attach def config: %w", err)
	}

	return string(modifiedConfig), nil
}

// setRuntimeConfigDeviceID ensures runtimeConfig.deviceID is set in one CNI config object.
func setRuntimeConfigDeviceID(config map[string]interface{}, deviceID string) error {
	runtimeConfig := config["runtimeConfig"]
	switch typedRuntimeConfig := runtimeConfig.(type) {
	case nil:
		config["runtimeConfig"] = map[string]interface{}{
			"deviceID": deviceID,
		}
	case map[string]interface{}:
		typedRuntimeConfig["deviceID"] = deviceID
	default:
		return fmt.Errorf("runtimeConfig must be a JSON object when present")
	}

	return nil
}
