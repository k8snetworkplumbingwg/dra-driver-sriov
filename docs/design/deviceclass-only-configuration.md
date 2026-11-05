# HLD: DeviceClass-only SR-IOV configuration

Status: Design proposal. No runtime changes are included in this document.

Implementation tracking: [#160](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/160). Merging this HLD does not complete the implementation issue.

Baseline: repository `main` at `dc62c4c89220ea6f5ed104634d5c1b78447216f6`. Source paths below describe that baseline; proposed interfaces and chart settings are implementation requirements, not existing features.

## Outcome and scope

Only administrator-managed `DeviceClass` objects will supply this driver's `VfConfig`. A workload can request an approved class, count, and narrower device selectors, but cannot change driver binding, host-device exposure, interface configuration, or network attachment by adding configuration to a `ResourceClaim` or `ResourceClaimTemplate`.

The driver will reject claim-sourced configuration addressed to `sriovnetwork.k8snetworkplumbingwg.io`. It will continue to ignore opaque configuration addressed to other drivers. Namespace access and consumption limits will be enforced by Kubernetes admission, RBAC, and per-class `ResourceQuota`, not by a new quota implementation inside the node driver.

This is a breaking security change. Silent fallback to class configuration would conceal incompatible manifests and potentially attach a different network than the workload expected. There will be no runtime switch that restores claim overrides in the hardened release.

Success means configuration-free tenant workloads can use an approved class end to end, while the same workloads cannot change its binding, device mounts, interface settings, or NRI network through claim configuration, a prepare retry, or driver restart. Class and claim selectors remain conjunctive: the claim can reduce the eligible set but cannot replace or widen the class selector.

Non-goals are a new allocation scheduler, a driver-owned quota controller, new PF/host-device discovery, a general Multus network authorization system, or removal of Kubernetes' opaque configuration API for other drivers. Durable-state storage and Multus/NRI route selection have separate HLDs; the integration contracts needed by this feature are specified here.

## Current behavior and affected code

| Location | Current behavior | Planned change |
| --- | --- | --- |
| `pkg/devicestate/statehelpers.go` | `getMapOfOpaqueDeviceConfigForDevice` separates `FromClass` and `FromClaim`, then applies claim configuration last. | Replace the precedence path with source validation and per-allocation resolution of class configuration. |
| `pkg/devicestate/state.go` | `PrepareDevicesForClaim` reads `claim.status.allocation.devices.config`; `prepareDevices` looks up an exact request name and then a global fallback. | Resolve all applicable class entries before host changes; support parent/subrequest scoping correctly. |
| `pkg/driver/dra_hook.go` | `prepareResourceClaim` returns cached devices before checking configuration. | Check the claim's configuration policy and saved provenance before returning prepared state. |
| `pkg/driver/dra_hook.go` | `PrepareResourceClaims` creates global Pod CDI from all cached Pod devices after individual prepare attempts. | Exclude rejected or unverified records from new CDI publication; do not let a cached record bypass validation through batch aggregation. |
| `pkg/api/virtualfunction/v1alpha1/api.go` | `VfConfig.Override` copies only `driver`, `ifName`, and `netAttachDefName`. It drops `netAttachDefNamespace` and `addVhostMount`, even for class configuration. | Preserve every field when accepting an administrator's configuration. Remove the incomplete merge path from preparation. |
| `pkg/devicestate/state.go` | A missing NAD namespace falls back to the claim namespace; the driver fetches the NAD through its own Kubernetes credentials. | Require an explicit NAD namespace for standalone network attachment in the hardened configuration contract. |
| `pkg/devicestate/statehelpers_test.go` | Tests require claim overrides and partial configuration merging. | Replace those expectations with rejection, trusted-class resolution, and complete-field preservation tests. |
| `deployments/helm/dra-driver-sriov/templates/deviceclass.yaml` | Creates a class selecting every advertised device for this driver, with no configuration. | Add explicit class-management settings and stop creating this broad class by default in the breaking release. |
| `deployments/helm/dra-driver-sriov/templates/clusterrole.yaml` | Driver can read claims and NADs, update claim status, and use `resourceclaims/driver`; it cannot manage classes or claim binding. | Preserve this separation; do not add class-write or binding permissions to the node driver. |

`README.md` and the following demo directories contain claim configuration or describe workload-controlled configuration and require migration: `claim-for-deployment`, `multiple-vf-claim`, `resource-alignment`, `resource-policies`, `resourceclaim`, `single-vf-claim`, and `vfio-driver`. The latter already has class configuration but also duplicates configuration in its claim template. Review the class-only examples in `extended-resource`, `host-device-kernel`, and `host-device-vfio` for explicit NAD namespaces. Review the Multus demos for approved class selection and administrator-owned NADs.

The disabled `*.yaml.bak` admission templates are not active security controls and target unrelated behavior. Add dedicated, rendered policies rather than describing those files as protection already deployed.

The driver currently discovers VFs in `pkg/devicestate/discovery.go`; it does not publish a built-in `deviceType` attribute. Do not add a fictional `deviceType: VF` field to examples. Use attributes actually published by discovery or administrator-owned `DeviceAttributes` policies. If a class selects VFs and a claim adds a contradictory PF/host-device selector on an available attribute, allocation remains unsatisfied; preparation must never reinterpret that selector as a configuration override.

## Trust boundary

### Allocation provenance

Use `ResourceClaim.status.allocation.devices.config` as the authoritative allocation-time configuration snapshot. Kubernetes distinguishes `FromClass` from `FromClaim` for precisely this provenance distinction. The current class object must not replace the snapshot during prepare: class edits after allocation must not silently reconfigure an already allocated device. The source marker is trusted only because API authorization protects allocation writes; it is not a signature. Request matching must handle an empty request list, a parent request, and `parent/subrequest`. [Kubernetes v0.36.3 allocation API](https://github.com/kubernetes/api/blob/v0.36.3/resource/v1/types.go#L1726).

The trusted set comprises cluster administrators, the scheduler or another authorized allocation controller, and the privileged node driver. Tenants must not gain write access to `DeviceClass`, `ResourceSlice`, driver policy/attribute objects, allocation status, runtime configuration, or host state. Protect namespace labels, quota objects, admission policies, and their parameter objects from tenant modification as well.

Validate these inputs before a prepared-state lookup can return success:

1. Reject any entry in `claim.spec.devices.config` with this driver's opaque driver name. This catches unsupported configuration independently of scheduler propagation, including entries scoped to unselected requests.
2. Inspect allocated configuration. Ignore a non-nil opaque entry naming another driver. For this driver, accept only `AllocationConfigSourceClass`; reject `AllocationConfigSourceClaim`, empty sources, and unknown sources. Reject unsupported non-opaque configuration instead of guessing its owner or meaning.
3. Resolve each SR-IOV allocation result to its claim request, including the selected `firstAvailable` subrequest. Preserve its class name as diagnostic provenance. Reject malformed references, mismatched local allocation identity, and a saved configuration fingerprint inconsistent with the allocation.
4. Resolve and validate the effective configuration for every result before invoking driver binding, module loading, CDI generation, device-info publication, or NRI/CNI attachment. Do not use `status.devices.data`, Pod annotations, or a cache record as an alternative source of user configuration.

Errors should identify claim namespace/name, config source, and request without logging arbitrary raw opaque parameters. A rejection must reach kubelet as a prepare error and create no new prepared-state record or host side effect. Cleanup is always allowed, including cleanup of claims that fail the new policy.

### API authorization

Kubernetes 1.36 introduces granular claim-status authorization, enabled by default in that release. Allocation and reservation changes require `resourceclaims/binding`; per-driver device status uses `resourceclaims/driver`. Retain the existing driver's `resourceclaims/status` and `associated-node:update` grant, and verify that its node association is accepted. Do not grant `binding` to the driver or tenants. Test authorization with real status updates; `kubectl auth can-i` alone does not exercise the field-sensitive checks. [DRA authorization hardening](https://kubernetes.io/docs/concepts/security/hardening-guide/dynamic-resource-allocation/).

The chart currently allows Kubernetes `>=1.34.0-0`, while `go.mod` uses Kubernetes libraries `v0.36.3` and the virtual cluster defaults to `1.36.1`. Retain the chart's general version floor in this feature; claim-configuration rejection applies on every supported version. The documented hardened deployment requires Kubernetes 1.36 with `DRAResourceClaimGranularStatusAuthorization` and its dependencies enabled. Older clusters need separately reviewed status-write restrictions and must not be presented as providing identical isolation. `DRAExtendedResource` is alpha/off by default in 1.34 and beta/on by default in 1.36. [Versioned feature definitions](https://github.com/kubernetes/kubernetes/blob/v1.36.3/pkg/features/kube_features.go#L1232).

### Network attachment is another trust boundary

An administrator-selected NAD name alone is insufficient if tenants can replace the named NAD in their namespace. Require `netAttachDefNamespace` whenever `netAttachDefName` is configured for standalone/NRI attachment, and place those NADs in an administrator-controlled namespace. Preserve both fields through decoding, prepared state, and recovery. A referenced NAD is still mutable: administrators must version NAD names for configuration changes, and prepared state must retain the resolved CNI configuration used for teardown.

In unified mode, the Multus annotation chooses the networking flow only. It cannot override `VfConfig` or grant VFIO/vhost access. Multus resolves its own NADs, so its admission/network isolation policy must separately constrain tenant NAD authoring and network selection. Verify that an annotated NAD's advertised resource name and allocated devices are compatible. DeviceClass RBAC and ResourceQuota alone do not authorize arbitrary Multus network attachment. The unified-mode plan owns route selection and annotation-mutation handling.

## Configuration resolution contract

Keep `sriovnetwork.k8snetworkplumbingwg.io/v1alpha1`, kind `VfConfig`, as the opaque payload schema. Remove support for the claim source, not the payload type or Kubernetes claim API fields. Tenant claim requests and constraints remain supported.

Adopt the following deterministic rules:

- Filter foreign-driver opaque configuration before applying SR-IOV source and payload validation.
- Validate this driver's configuration even when its request scope was not selected. Unsupported SR-IOV claim configuration is rejected consistently at admission and preparation.
- For each allocation result, select class entries whose `requests` is empty, names that result's parent request, or names the exact `parent/subrequest`. An exact match must not leak configuration from an unselected sibling.
- Accept one applicable SR-IOV class entry per allocation result and deep-copy the complete decoded `VfConfig`. Reject overlapping entries with a clear administrator-facing error. This removes the existing incomplete field merge and avoids inventing unset-versus-false semantics for `addVhostMount`.
- When no SR-IOV class entry applies, retain built-in defaults. This supports Multus classes needing no device-binding override. Standalone kernel networking still fails without a NAD; VFIO without CNI still requires an administrator-provided `driver: vfio-pci`.
- Allocate a separate configuration instance per device before assigning an automatic interface name. Prefer automatic names for classes that can allocate several devices; an administrator-specified fixed `ifName` must fail clearly if it would collide.
- Validate all fields as a complete contract, including supported driver values, interface names, NAD name/namespace pairing, and permitted mount combinations. Fixing the dropped `addVhostMount` field can activate previously ineffective administrator configuration, so call this out in migration notes.

Rejecting overlapping class entries is an additional compatibility change. It is chosen to keep this release's security behavior reviewable without introducing a new patch/merge API. If existing users require class layering, design a versioned presence-aware schema as separate work rather than preserving silently dropped fields.

### Preparation boundary

Implement a side-effect-free resolver in `pkg/devicestate` with a contract equivalent to `ResolveClaimConfiguration(claim) (ResolvedClaimConfiguration, error)`. Its output has a claim-wide canonical configuration fingerprint and one entry per allocation result belonging to this driver, keyed by `(request, driver, pool, device, shareID)`, containing the selected request/class name and a complete `VfConfig`. It must validate every same-driver spec and allocation configuration before returning any usable output. A foreign driver's result must not receive SR-IOV defaults or appear in this map.

`pkg/driver/dra_hook.go` calls this resolver before its prepared-state fast path and passes the resolved object into `PrepareDevicesForClaim`; the device manager no longer resolves configuration by map fallback while mutating hardware. Resolve all local device identities, field validity, interface-name conflicts, and required NAD references before the first bind. API reads may happen during this preflight, but host, CDI, publication, and state writes may not. Existing rollback remains responsible for failures during subsequent device operations.

Preserve the current binding contract: empty `driver` leaves binding unchanged, `default` restores the device's default kernel driver, and an explicit Linux driver name requests that binding. Validate explicit names as a single driver identifier, never a filesystem path. VFIO device-node exposure remains conditional on `driver: vfio-pci`; this feature does not introduce arbitrary host paths or a new mount API. `addVhostMount` remains an administrator-controlled boolean using the existing fixed device nodes. Nonempty `ifName` must be a valid Linux interface name and unique among attachments in the same Pod; empty names are assigned without mutating the trusted source config. A NAD namespace without a name is invalid. For NRI kernel attachment both fields are required; VFIO without CNI may omit both. In the Multus route both may be absent, and a class NAD must not cause an NRI attachment.

Fingerprint a versioned canonical serialization of the claim UID, local allocation keys, selected class names, and complete resolved configurations, sorted by allocation key. Exclude mutable `status.devices`, resource versions, raw JSON whitespace, and generated interface names. Keep generated names and resolved NAD contents separately as applied/cleanup state. The fingerprint binds a retry to the same trusted allocation; it is not a signature or a substitute for API authorization.

For a batch containing rejected claims, return their individual prepare errors and aggregate global Pod CDI only from records with current verified provenance. Do not include rejected cached records, and do not delete their existing artifacts merely to report rejection: they may still be needed for cleanup of running legacy workloads. If publication cannot safely exclude legacy records while preserving live cleanup, fail that Pod's new preparation with a migration error. This closes both the individual cache and Pod-wide aggregation paths.

## Administrator and workload manifests

The administrator owns the class and the NAD. This example assumes `sriov-system/tenant-blue-net` already exists, is administrator-controlled, and is compatible with the selected devices:

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: sriov-blue
spec:
  selectors:
  - cel:
      expression: >-
        device.driver == 'sriovnetwork.k8snetworkplumbingwg.io' &&
        device.attributes['k8s.cni.cncf.io'].resourceName == 'blue_vfs'
  config:
  - opaque:
      driver: sriovnetwork.k8snetworkplumbingwg.io
      parameters:
        apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
        kind: VfConfig
        netAttachDefName: tenant-blue-net
        netAttachDefNamespace: sriov-system
---
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: blue-vf-v2
  namespace: tenant-blue
spec:
  spec:
    devices:
      requests:
      - name: network
        exactly:
          deviceClassName: sriov-blue
          allocationMode: ExactCount
          count: 1
          selectors:
          - cel:
              expression: device.attributes['sriovnetwork.k8snetworkplumbingwg.io'].vfID == 0
```

`DeviceAttributes` and `SriovResourcePolicy` must publish the `blue_vfs` attribute used above. The workload's `vfID == 0` selector reduces the class's eligible VF set; removing that claim selector allows any VF already eligible for the class. Adding `resourceName == 'red_vfs'` instead would contradict the class and leave allocation unsatisfied. Tenants cannot widen the class selectors or replace its configuration. Create separate administrator-managed classes for different network or binding policies, even if they select overlapping physical hardware.

## Namespace access and ResourceQuota

A DeviceClass is cluster-scoped. RBAC restricting its creation does not itself restrict which namespaces may reference it. Kubernetes class quota uses the exact key `<class-name>.deviceclass.resource.k8s.io/devices`. This counts requested devices, not claim objects or a named PCI function. A missing class quota does not deny access. [DeviceClass API](https://kubernetes.io/docs/reference/kubernetes-api/resource/device-class-v1/), [ResourceQuota documentation](https://kubernetes.io/docs/concepts/policy/resource-quotas/).

For an approved namespace:

```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: sriov-classes
  namespace: tenant-blue
spec:
  hard:
    sriov-blue.deviceclass.resource.k8s.io/devices: "4"
    sriov-vfio.deviceclass.resource.k8s.io/devices: "0"
    count/resourceclaims.resource.k8s.io: "16"
```

An explicit zero denies positive requests for that class. Object-count quota is an additional claim-count limit; it is not a device limit. Do not use a fictional `DeviceClass` scope selector or an extended-resource `requests.*` key in place of the class device key for the basic claim example.

Quota accounting uses requested `ExactCount` values even before allocation. `firstAvailable` charges the largest requested count for each candidate class, and `All` uses the API's maximum allocation-result count. Templates consume device quota when their claims are created. Reusing a claim does not multiply its claim device usage. Quota limits do not evict existing users when lowered. With DRA extended resources enabled, include both explicit claims and implicit/custom extended-resource Pod requests in the acceptance tests, including protection against double accounting. [Kubernetes 1.36 claim quota evaluator](https://github.com/kubernetes/kubernetes/blob/v1.36.3/pkg/quota/v1/evaluator/core/resource_claims.go).

The class device key is already evaluated by Kubernetes 1.34 and requires no separate SR-IOV feature gate. Enable normal ResourceQuota admission and quota reconciliation; validate actual admission behavior on every supported cluster version. [Kubernetes 1.34 claim quota evaluator](https://github.com/kubernetes/kubernetes/blob/v1.34.0/pkg/quota/v1/evaluator/core/resource_claims.go).

Class limits do not add together automatically for different classes selecting the same devices. Remove or deny the chart's broad `sriovnetwork.k8snetworkplumbingwg.io` class in restricted namespaces, inventory alternative classes and extended-resource aliases, and keep approved classes' selectors appropriately narrow. Quota controls access through the named class, not a universal ACL on each VF. Claim `adminAccess`, namespace admin-access labels, and privileged workload permissions require their own restrictions.

## Admission and deployment changes

Ship dedicated `admissionregistration.k8s.io/v1` ValidatingAdmissionPolicies and bindings for `ResourceClaim` and `ResourceClaimTemplate` create/update. Their guarded CEL expressions reject this driver's opaque configuration at `object.spec.devices.config` and `object.spec.spec.devices.config`, respectively; absent config and other-driver entries remain valid. Use `failurePolicy: Fail`, `validationActions: [Deny]`, and equivalent-version matching. Keep status-subresource writes outside these spec policies so existing claims can be cleaned up. These policies provide earlier feedback; driver-side rejection remains mandatory if a policy is absent. [ValidatingAdmissionPolicy reference](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/).

For claims, the guarded CEL condition is equivalent to:

```cel
(request.operation == 'UPDATE' && object.spec == oldObject.spec) ||
!has(object.spec.devices.config) ||
object.spec.devices.config.all(c,
  !has(c.opaque) || c.opaque.driver != 'sriovnetwork.k8snetworkplumbingwg.io')
```

Use the same condition at `object.spec.spec.devices.config` for templates, retaining the unchanged-`spec` update guard. That guard permits metadata-only updates to legacy objects, including removing finalizers during cleanup; it must not permit creation or a spec change carrying this driver's configuration. It does not authorize fresh preparation of a legacy claim: driver validation still rejects it. Match only the main resources, not `resourceclaims/status`, and test compiled CEL against actual admitted objects.

Add these explicit Helm settings and document them in the values reference:

| Proposed setting | Default and contract |
| --- | --- |
| `deviceClasses.createDefault` | `false`; setting `true` explicitly creates the historical broad driver class. Do not retain unconditional creation. |
| `deviceClasses.items` | `[]`; administrator-supplied entries with `name` and complete `spec`, rendered as DeviceClasses. Validate name uniqueness and reject collision with the broad class. An empty list is valid when classes are managed outside Helm. |
| `admission.claimConfiguration.enabled` | `true`; renders both policies and their bindings. Disabling rendering requires externally managed equivalent policies for a hardened deployment. |
| `admission.claimConfiguration.mode` | `Deny`; the only alternate value is `Audit`, which renders `[Audit, Warn]` for pre-migration inventory. This does not change the driver's rejection behavior. |

An optional pre-migration installation can use `Audit`/`Warn` to identify affected workloads before rolling out the class-only binary. The hardened deployment uses denial and verifies installed policy bindings; it does not infer enforcement from successful Helm rendering alone. Render and validate policies against the oldest supported admission API rather than copying the existing backup templates.

Separate administrator installation examples from tenant workload manifests. Add chart settings for explicit class creation/listing, documenting that an installer needs cluster-scoped class and admission-policy permissions but the node driver service account does not. Make the broad default class opt-in in the breaking release. Namespace-specific quotas remain administrator-managed examples or explicit values, with no implied global enforcement from merely installing the chart.

## Migration and interaction with durable state

1. Inventory ResourceClaims, templates, class configuration, referenced NAD ownership, alternative classes, and namespace quota coverage. Identify duplicate class entries and configurations relying on the dropped fields.
2. Create administrator-managed NADs and versioned classes. Create replacement templates and claims with configuration removed, and update workloads to reference them. Existing claim/template specs are immutable; migration cannot patch away their configuration. [Kubernetes claim and template types](https://github.com/kubernetes/api/blob/v0.36.3/resource/v1/types.go#L806).
3. Deploy and validate RBAC, admission policies, and namespace quotas, including explicit zero limits for classes a namespace must not use.
4. Drain and replace affected workloads in a controlled rollout. Class edits do not rewrite existing allocation snapshots. New preparation of a legacy claim must fail with a migration-specific message.
5. Deploy the class-only driver and validate mixed-driver claims, cleanup, restart, and both network routes before expanding rollout.

Persist a configuration-policy/schema version and the trusted allocation/configuration fingerprint with prepared records. The bbolt prepared-state HLD owns the storage implementation; these are per-claim trust fields, not a separate migration ledger or database metadata bucket. If this feature lands first, add equivalent per-record fields to the existing checkpoint representation through `pkg/types` and `pkg/podmanager`, then carry them unchanged into the bbolt JSON record. Migration must not label an old effective configuration as class-sourced merely because it matches current class values. Verify provenance from the claim's allocation snapshot; compare the applied configuration and preserve any mismatch for recovery review. API unavailability must not promote unverified records to trusted prepared state.

Keep legacy cleanup metadata so existing device bindings and network attachments can be removed. Do not replay old claim-controlled configuration into a new container or new attachment. Where live, already attached workloads are retained during upgrade, records must distinguish cleanup-only legacy state from state authorized for fresh use. The simplest supported security rollout drains legacy workloads before enabling enforcement. Rollback to an older driver reintroduces claim overrides and must not be described as maintaining the new security guarantee.

## Implementation sequence

1. Add source-policy validation and request-scope resolution with no external side effects. Replace the incomplete merge during preparation with complete class-config copies and overlap rejection.
2. Integrate validation before the driver's prepared-state fast path and before device-state preparation. Ensure all claim configs are checked before preparing any device in the claim. Preserve cleanup and foreign-driver compatibility.
3. Add NAD namespace validation and complete-field persistence. Align the trusted-configuration record with the bbolt plan; align networking validation with unified-mode routing.
4. Add active admission templates, deliberate class-management chart settings, administrator RBAC examples, namespace ResourceQuota examples, and version prerequisites.
5. Migrate all demos and README/Helm documentation, including extended-resource examples and multiple-interface guidance.
6. Add the tests below to existing package suites and the virtual-cluster workflow. Document the breaking change and a migration checklist with observable pass/fail conditions.

## Test and acceptance matrix

| Layer | Required evidence |
| --- | --- |
| Resolver unit tests | Accept one class entry; preserve all five `VfConfig` fields; reject same-driver claim config and unknown source; ignore foreign-driver opaque entries; reject malformed payload/unsupported shape; reject overlapping applicable class entries. |
| Request-scoping tests | Empty scope, exact request, parent request, selected subrequest, unselected sibling, multiple classes, and multiple devices produce the expected isolated configuration. Automatic names do not mutate shared configs. |
| Driver tests | A rejected claim causes zero host/CDI/CNI/store mutations; a cached or migrated record cannot bypass rejection, including Pod-wide global CDI aggregation after mixed batch success/failure; a valid class-only retry is idempotent; old claims can still unprepare. |
| NAD tests | Explicit administrator namespace is honored; omitted namespace fails for attachment; tenant-local same-name NAD does not replace the administrator NAD; persisted teardown uses the original resolved configuration. |
| Admission integration | Reject creation/spec changes carrying SR-IOV opaque config in direct claims and templates; allow other-driver config and configuration-free claims; allow legacy metadata-only/finalizer cleanup updates without allowing fresh prepare; verify status cleanup and equivalent API-version handling. |
| RBAC integration | Tenant cannot write classes, trusted NADs, quotas, protected namespace labels, slices, or allocation/binding status. Driver can update its device status and cannot forge allocation provenance or modify another driver's status. |
| Quota integration | Allowed namespace requests up to its limit; next request fails; explicit-zero quota denies the class; absence of a class quota does not deny access; deleted claim releases usage; replacement claims/templates behave correctly; requested-device and object-count limits remain distinct. |
| Allocation variants | Test `firstAvailable`, `All`, two class names selecting overlapping devices, broad-class bypass attempts, custom extended-resource requests, and `deviceclass.resource.kubernetes.io/<class>` requests under the chosen version/gates. |
| Selector boundary | A class selecting `blue_vfs` plus a claim's valid `vfID` selector allocates only their intersection; contradictory `red_vfs` selection remains unallocated. If test inventory includes PF/host-device attributes, a VF-only class plus contradictory claim type selector cannot allocate those devices. |
| Helm integration | Default chart creates no broad class and renders both deny policies; explicit class list, broad opt-in, externally managed classes, and audit mode render as documented. Apply rendered policies and class/claim/quota examples to a real API server, including the chart's supported admission versions. |
| End to end | Class-only standalone, VFIO without CNI, and Multus workloads succeed; claim overrides fail before driver binding or attachment; restart preserves accepted class config; upgrade never replays rejected legacy configuration. |

Add a dedicated class-configuration/security scenario in `test/e2e/` using `test/e2e/framework/fixtures.go`, the existing apply/cleanup/wait helpers, and a label selectable with `E2E_LABEL_FILTER`. Extend `.github/workflows/virtual-e2e-singlenode.yaml` and `.github/workflows/virtual-e2e-multinode.yaml` through the existing `run-e2e-workloads` action; do not create an independent cluster harness. Hardware-dependent VFIO and network tests must report explicit prerequisite skips; admission, quota, and selector tests must run in every applicable cluster lane.

After implementation run `go test ./pkg/devicestate ./pkg/driver ./pkg/types ./pkg/podmanager`, `make lint`, and `make test`, then `make e2e-workloads E2E_LABEL_FILTER=<new-security-label>` on the virtual cluster. Admission, RBAC, quota, and scheduling guarantees require a real API server and scheduler; mocked client tests cannot establish them. Record actual Kubernetes version/gates and test results in the implementation PR. No runtime tests are required merely to add this HLD.

If bbolt has already replaced PodManager, use `./pkg/preparedstate/...` instead of `./pkg/podmanager` in the focused test command. The same provenance and cleanup assertions apply to the active backend.

## Dependencies, risks, and completion criteria

This proposal can merge independently of the bbolt and unified-mode HLDs. Its implementation requires the provenance contract on whichever prepared-state backend is active, and policy checks on both active network routes. Runtime missing-plugin enforcement is a separate availability/control-path guarantee; it neither grants class access nor validates `VfConfig` provenance.

The feature is complete when all acceptance rows pass, migrated demos run without same-driver claim configuration, fresh prepare and cached retries use the same resolver, cleanup of legacy state succeeds, and release documentation states the explicit quota and Kubernetes authorization prerequisites. Do not close the implementation issue merely because this HLD merges.

- Requiring explicit NAD namespaces, rejecting overlapping class entries, and disabling automatic broad-class creation increase migration work. These changes make the administrator-only promise concrete and must share the same release notes as claim-config removal.
- Existing broad classes can expose devices through an alternate quota key. Class inventory and explicit namespace quotas are acceptance requirements for multi-tenant deployments; missing class quotas do not deny access.
- Preexisting live attachments cannot become safe through database schema migration alone. Preserve teardown state and choose a documented drain schedule; never silently relabel their provenance.
- Admin-managed class configuration does not remove all privileged device risk. VFIO exposure, IOMMU isolation, CNI/NAD content, and Multus network selection remain separate administrator responsibilities.
- The general chart floor remains 1.34, while the hardened isolation claim requires verified 1.36 status authorization. A future project-wide version-floor change is separate from this feature.
