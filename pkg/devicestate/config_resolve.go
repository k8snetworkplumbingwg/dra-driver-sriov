package devicestate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"

	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// AllocationKey uniquely identifies one local allocation result for this driver.
type AllocationKey struct {
	Request string
	Driver  string
	Pool    string
	Device  string
	ShareID string
}

// String formats allocation identity for diagnostics and canonical sorting.
func (k AllocationKey) String() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s", k.Request, k.Driver, k.Pool, k.Device, k.ShareID)
}

// ResolvedDeviceConfiguration holds the trusted class-sourced config for one allocation result.
type ResolvedDeviceConfiguration struct {
	Key         AllocationKey
	RequestName string
	ClassName   string
	Config      *configapi.VfConfig
}

// ResolvedClaimConfiguration is the output of successful claim configuration resolution.
type ResolvedClaimConfiguration struct {
	ClaimUID            k8stypes.UID
	ConfigPolicyVersion string
	ConfigurationMode   string
	Fingerprint         string
	ByKey               map[AllocationKey]ResolvedDeviceConfiguration
	// ExistingIfNames maps reserved names to their claim owners, shared per Pod.
	ExistingIfNames map[string]k8stypes.UID
}

// VerifyPreparedClaim checks saved provenance and device identity against freshly resolved configuration.
func VerifyPreparedClaim(claim *resourceapi.ResourceClaim, resolved *ResolvedClaimConfiguration, devices types.PreparedDevices, meta types.PreparedClaimMeta) error {
	invalid := func() error {
		return fmt.Errorf("migration: prepared state not trusted for claim %s/%s", claim.Namespace, claim.Name)
	}
	if meta.LegacyCleanupOnly || meta.ConfigPolicyVersion != resolved.ConfigPolicyVersion || meta.ConfigFingerprint != resolved.Fingerprint || claim.UID != resolved.ClaimUID || len(devices) != len(resolved.ByKey) {
		return invalid()
	}
	seen := make(map[AllocationKey]bool, len(devices))
	for _, device := range devices {
		if device == nil || len(claim.Status.ReservedFor) != 1 || device.PodUID != string(claim.Status.ReservedFor[0].UID) || device.ClaimNamespacedName.UID != claim.UID || device.ClaimNamespacedName.Name != claim.Name || device.ClaimNamespacedName.Namespace != claim.Namespace || len(device.Device.RequestNames) != 1 || device.Config == nil {
			return invalid()
		}
		key := AllocationKey{Request: device.Device.RequestNames[0], Driver: consts.DriverName, Pool: device.Device.PoolName, Device: device.Device.DeviceName, ShareID: device.ShareID}
		entry, ok := resolved.ByKey[key]
		if !ok || seen[key] || vfConfigFingerprintFields(device.Config) != vfConfigFingerprintFields(entry.Config) {
			return invalid()
		}
		seen[key] = true
		if entry.Config.IfName != "" && device.IfName != entry.Config.IfName {
			return invalid()
		}
	}
	return nil
}

// ResolveClaimConfiguration validates class-only allocation snapshots and fingerprints independent per-device configurations.
func ResolveClaimConfiguration(
	claim *resourceapi.ResourceClaim,
	decoder runtime.Decoder,
	configurationMode string,
) (*ResolvedClaimConfiguration, error) {
	if claim == nil {
		return nil, fmt.Errorf("claim is nil")
	}
	configurationMode, err := NormalizeConfigurationMode(configurationMode)
	if err != nil {
		return nil, err
	}
	if err := rejectClaimSpecSRIOVConfig(claim.Spec.Devices.Config); err != nil {
		return nil, fmt.Errorf("claim %s/%s: %w", claim.Namespace, claim.Name, err)
	}
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim %s/%s: not allocated", claim.Namespace, claim.Name)
	}

	classConfigs, err := collectTrustedClassConfigs(claim.Status.Allocation.Devices.Config, decoder)
	if err != nil {
		return nil, fmt.Errorf("claim %s/%s: %w", claim.Namespace, claim.Name, err)
	}
	for i := range classConfigs {
		if err := configapi.ValidateForPrepare(configurationMode, classConfigs[i].config); err != nil {
			return nil, fmt.Errorf("claim %s/%s: class configuration source %q for requests %v: %w", claim.Namespace, claim.Name, resourceapi.AllocationConfigSourceClass, classConfigs[i].requests, err)
		}
	}

	byKey := make(map[AllocationKey]ResolvedDeviceConfiguration)
	selectedRequests := make(map[string]string)
	for i := range claim.Status.Allocation.Devices.Results {
		result := &claim.Status.Allocation.Devices.Results[i]
		if result.Driver != consts.DriverName {
			continue
		}
		className, err := classForRequest(claim, result.Request)
		if err != nil {
			return nil, fmt.Errorf("claim %s/%s request %q: %w", claim.Namespace, claim.Name, result.Request, err)
		}
		parent, _, _ := strings.Cut(result.Request, "/")
		if selected, exists := selectedRequests[parent]; exists && selected != result.Request {
			return nil, fmt.Errorf("claim %s/%s request %q: allocation selects multiple firstAvailable subrequests", claim.Namespace, claim.Name, parent)
		}
		selectedRequests[parent] = result.Request
		if result.Pool == "" || result.Device == "" {
			return nil, fmt.Errorf("claim %s/%s request %q: empty allocation pool or device", claim.Namespace, claim.Name, result.Request)
		}
		cfg, err := resolveConfigForResult(result.Request, classConfigs)
		if err != nil {
			return nil, fmt.Errorf("claim %s/%s request %q: %w", claim.Namespace, claim.Name, result.Request, err)
		}
		if err := configapi.ValidateForPrepare(configurationMode, cfg); err != nil {
			return nil, fmt.Errorf("claim %s/%s request %q: %w", claim.Namespace, claim.Name, result.Request, err)
		}
		key := allocationKeyFromResult(result)
		if _, duplicate := byKey[key]; duplicate {
			return nil, fmt.Errorf("claim %s/%s: duplicate allocation %s", claim.Namespace, claim.Name, key)
		}
		byKey[key] = ResolvedDeviceConfiguration{
			Key:         key,
			RequestName: result.Request,
			ClassName:   className,
			Config:      cfg,
		}
	}

	if err := validateFixedIfNameUniqueness(byKey); err != nil {
		return nil, fmt.Errorf("claim %s/%s: %w", claim.Namespace, claim.Name, err)
	}

	resolved := &ResolvedClaimConfiguration{
		ClaimUID:            claim.UID,
		ConfigPolicyVersion: consts.DeviceClassAdminConfigPolicyVersion,
		ConfigurationMode:   configurationMode,
		ByKey:               byKey,
	}
	resolved.Fingerprint, err = fingerprintClaimConfiguration(resolved)
	if err != nil {
		return nil, fmt.Errorf("claim %s/%s: %w", claim.Namespace, claim.Name, err)
	}
	return resolved, nil
}

// classForRequest returns the class selected by an exact or parent/subrequest allocation reference.
func classForRequest(claim *resourceapi.ResourceClaim, reference string) (string, error) {
	parts := strings.Split(reference, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return "", fmt.Errorf("malformed request reference")
	}
	for _, request := range claim.Spec.Devices.Requests {
		if request.Name != parts[0] {
			continue
		}
		if len(parts) == 1 && request.Exactly != nil && request.Exactly.DeviceClassName != "" {
			return request.Exactly.DeviceClassName, nil
		}
		if len(parts) == 2 && request.Exactly == nil {
			for _, sub := range request.FirstAvailable {
				if sub.Name == parts[1] && sub.DeviceClassName != "" {
					return sub.DeviceClassName, nil
				}
			}
		}
		return "", fmt.Errorf("allocation does not identify an exact request or a selected firstAvailable subrequest")
	}
	return "", fmt.Errorf("allocation references an unknown claim request")
}

// rejectClaimSpecSRIOVConfig rejects this driver's claim configuration regardless of request scope.
func rejectClaimSpecSRIOVConfig(configs []resourceapi.DeviceClaimConfiguration) error {
	for _, c := range configs {
		if c.Opaque == nil {
			continue
		}
		if c.Opaque.Driver == consts.DriverName {
			return fmt.Errorf("opaque configuration for driver %s is not allowed in claim spec (source %q, requests %v)", consts.DriverName, resourceapi.AllocationConfigSourceClaim, c.Requests)
		}
	}
	return nil
}

type decodedClassConfig struct {
	requests []string
	config   *configapi.VfConfig
}

// collectTrustedClassConfigs decodes FromClass entries for this driver and ignores foreign-driver entries.
func collectTrustedClassConfigs(
	configs []resourceapi.DeviceAllocationConfiguration,
	decoder runtime.Decoder,
) ([]decodedClassConfig, error) {
	var classConfigs []decodedClassConfig
	for _, cfg := range configs {
		if cfg.Opaque == nil {
			return nil, fmt.Errorf("allocation configuration source %q requires opaque parameters for requests %v", cfg.Source, cfg.Requests)
		}
		if cfg.Opaque.Driver != consts.DriverName {
			continue
		}
		switch cfg.Source {
		case resourceapi.AllocationConfigSourceClass:
		case resourceapi.AllocationConfigSourceClaim:
			return nil, fmt.Errorf("allocation configuration source FromClaim is not allowed for driver %s for requests %v", consts.DriverName, cfg.Requests)
		case "":
			return nil, fmt.Errorf("allocation configuration for driver %s has empty source for requests %v", consts.DriverName, cfg.Requests)
		default:
			return nil, fmt.Errorf("allocation configuration for driver %s has unknown source %q for requests %v", consts.DriverName, cfg.Source, cfg.Requests)
		}

		decoded, err := runtime.Decode(decoder, cfg.Opaque.Parameters.Raw)
		if err != nil {
			// Kubernetes decoder errors can embed the entire opaque payload.
			return nil, fmt.Errorf("invalid VfConfig payload from source %q for requests %v: check apiVersion, kind and schema", cfg.Source, cfg.Requests)
		}
		vfConfig, ok := decoded.(*configapi.VfConfig)
		if !ok {
			return nil, fmt.Errorf("decoded config from source %q for requests %v is not a VfConfig", cfg.Source, cfg.Requests)
		}
		classConfigs = append(classConfigs, decodedClassConfig{
			requests: append([]string(nil), cfg.Requests...),
			config:   vfConfig.DeepCopy(),
		})
	}
	return classConfigs, nil
}

// resolveConfigForResult returns one complete matching config or fresh defaults, rejecting overlaps.
func resolveConfigForResult(resultRequest string, classConfigs []decodedClassConfig) (*configapi.VfConfig, error) {
	var matched []decodedClassConfig
	for _, entry := range classConfigs {
		if configAppliesToRequest(entry.requests, resultRequest) {
			matched = append(matched, entry)
		}
	}
	switch len(matched) {
	case 0:
		return configapi.DefaultVfConfig(), nil
	case 1:
		return matched[0].config.DeepCopy(), nil
	default:
		return nil, fmt.Errorf("overlapping class configuration entries for request %q", resultRequest)
	}
}

// configAppliesToRequest matches global, parent-request, or exact subrequest scopes.
func configAppliesToRequest(entryRequests []string, resultRequest string) bool {
	if len(entryRequests) == 0 {
		return true
	}
	parent, _, _ := strings.Cut(resultRequest, "/")
	for _, r := range entryRequests {
		if r == resultRequest {
			return true
		}
		if parent != "" && r == parent {
			return true
		}
	}
	return false
}

// allocationKeyFromResult builds the request/driver/pool/device/share identity for an allocation.
func allocationKeyFromResult(result *resourceapi.DeviceRequestAllocationResult) AllocationKey {
	share := ""
	if result.ShareID != nil {
		share = string(*result.ShareID)
	}
	return AllocationKey{
		Request: result.Request,
		Driver:  result.Driver,
		Pool:    result.Pool,
		Device:  result.Device,
		ShareID: share,
	}
}

// validateFixedIfNameUniqueness rejects duplicate explicit interface names within a claim.
func validateFixedIfNameUniqueness(byKey map[AllocationKey]ResolvedDeviceConfiguration) error {
	seen := make(map[string]AllocationKey)
	for key, resolved := range byKey {
		name := resolved.Config.IfName
		if name == "" {
			continue
		}
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("interface name %q used by %s and %s", name, prev, key)
		}
		seen[name] = key
	}
	return nil
}

type fingerprintDevice struct {
	Key       AllocationKey           `json:"key"`
	ClassName string                  `json:"className"`
	Config    configFingerprintFields `json:"config"`
}

type configFingerprintFields struct {
	Driver                string `json:"driver,omitempty"`
	AddVhostMount         bool   `json:"addVhostMount,omitempty"`
	IfName                string `json:"ifName,omitempty"`
	NetAttachDefName      string `json:"netAttachDefName,omitempty"`
	NetAttachDefNamespace string `json:"netAttachDefNamespace,omitempty"`
}

// vfConfigFingerprintFields extracts policy fields, excluding generated names and runtime state.
func vfConfigFingerprintFields(cfg *configapi.VfConfig) configFingerprintFields {
	if cfg == nil {
		return configFingerprintFields{}
	}
	return configFingerprintFields{
		Driver:                cfg.Driver,
		AddVhostMount:         cfg.AddVhostMount,
		IfName:                cfg.IfName,
		NetAttachDefName:      cfg.NetAttachDefName,
		NetAttachDefNamespace: cfg.NetAttachDefNamespace,
	}
}

// fingerprintClaimConfiguration hashes sorted allocation identities, class names, policy version, mode, and configuration.
func fingerprintClaimConfiguration(resolved *ResolvedClaimConfiguration) (string, error) {
	keys := make([]AllocationKey, 0, len(resolved.ByKey))
	for k := range resolved.ByKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].String() < keys[j].String()
	})

	devices := make([]fingerprintDevice, 0, len(keys))
	for _, k := range keys {
		entry := resolved.ByKey[k]
		devices = append(devices, fingerprintDevice{
			Key:       k,
			ClassName: entry.ClassName,
			Config:    vfConfigFingerprintFields(entry.Config),
		})
	}

	payload := struct {
		PolicyVersion string              `json:"policyVersion"`
		Mode          string              `json:"mode"`
		ClaimUID      k8stypes.UID        `json:"claimUID"`
		Devices       []fingerprintDevice `json:"devices"`
	}{
		PolicyVersion: resolved.ConfigPolicyVersion,
		Mode:          resolved.ConfigurationMode,
		ClaimUID:      resolved.ClaimUID,
		Devices:       devices,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("fingerprint marshal: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
