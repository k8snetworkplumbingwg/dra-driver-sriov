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

	runtimeConfig := rawConfig["runtimeConfig"]
	switch typedRuntimeConfig := runtimeConfig.(type) {
	case nil:
		rawConfig["runtimeConfig"] = map[string]interface{}{
			"deviceID": deviceID,
		}
	case map[string]interface{}:
		typedRuntimeConfig["deviceID"] = deviceID
	default:
		return "", fmt.Errorf("runtimeConfig must be a JSON object when present")
	}

	modifiedConfig, err := json.Marshal(rawConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal net attach def config: %w", err)
	}

	return string(modifiedConfig), nil
}
