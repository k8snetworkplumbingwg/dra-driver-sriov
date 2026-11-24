# HLD: Required NRI plugin enforcement in CRI-O and containerd

Status: proposed. This document defines the implementation and acceptance contract; runtime tests have not been executed for this proposal.

Source baseline: repository `main` at `dc62c4c89220ea6f5ed104634d5c1b78447216f6`.

Tracking issue: [#166](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/166). Dependencies: [DeviceClass-only configuration #160](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/160), [bbolt prepared state #161](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/161), and [unified operation mode #162](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/162).

## Problem, scope, and decisions

A DRA allocation and a prepared CDI device do not prove that the runtime called the driver's NRI networking hook. If the driver is disconnected, a runtime without required-plugin enforcement can create a workload container without the intended networking. Merely observing a Pending Pod while the DRA process is down cannot demonstrate protection at the runtime boundary.

The default hardened profile is **WORKLOAD**: mandatory admission adds this driver's NRI requirement to selected Pods, and the runtime's built-in default validator rejects their container creation when the plugin is missing. Selection is deliberately conservative: all explicit DRA-claim Pods, all requests using the automatic DeviceClass extended-resource prefix, and all requests using registered custom DRA aliases. Selection can include Pods served exclusively by another DRA driver. It does not include ordinary platform Pods or unrelated legacy device-plugin resources. Generic DeviceClass CEL selectors do not provide a reliable static ownership declaration, so this proposal does not infer ownership by parsing selector strings.

**NODE** is an explicit administrator opt-in: globally require the plugin for every new container on configured nodes, with protected bootstrap exemptions. **OFF** is an explicit migration opt-out with no required-plugin guarantee. Helm and CLI default to workload; deployment preflight must reject missing admission or runtime prerequisites rather than silently downgrade. This changes the previous unenforced installation behavior, so upgrades must stage admission/runtime support before rolling the default profile. The selected profile applies equally to Multus-owned and NRI-owned consumers in unified mode.

The guarantees have three parts:

1. A selected container requires the configured NRI plugin to participate in its `CreateContainer` operation.
2. A container consuming this driver's NRI-owned network devices requires committed attachment and required metadata for its actual sandbox.
3. A fatal disconnect or timeout during that readiness callback cannot satisfy the same creation request's required-plugin check.

The boundary is container creation. This validator does not independently deny `RunPodSandbox`, revoke containers already created, stop running containers, or revalidate a later `StartContainer`. A sandbox can exist during an outage. Stronger start-time revocation is outside this feature. [Requested NRI validator revision](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/plugins/default-validator/default-validator.go), [NRI event adaptation](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/pkg/adaptation/adaptation.go).

Dependencies are the DeviceClass-only configuration proposal, the bbolt prepared-state proposal, and the unified operation-mode proposal. Their required contracts are restated below so this document can be reviewed independently. This feature does not introduce another prepared-state store, run a separate validator DaemonSet, authenticate plugins by name, or silently change host runtime configuration from a privileged driver Pod.

## Current implementation and change locations

| Location | Current behavior | Implementation responsibility |
| --- | --- | --- |
| `pkg/nri/nri.go` | Handles sandbox run/stop; missing prepared devices can return success; connection closure cancels the main process. | Add readiness checks and synchronization using durable state; distinguish unrelated Pods from unresolved consumers. |
| `pkg/driver/dra_hook.go` | Prepares claims without a protected runtime requirement. | Validate the workload marker before every fresh or cached prepare success. |
| `pkg/driver/health.go` | Implements the empty and `liveness` gRPC services; no distinct readiness service exists. | Add `readiness` using the shared store/recovery/NRI state contract. |
| `cmd/dra-driver-sriov/main.go`, `pkg/types/config.go` | Legacy `MULTUS` skips NRI registration. | Add enforcement-profile configuration and startup validation; reject enforcement with legacy `MULTUS`. |
| `deployments/helm/dra-driver-sriov` | Supplies plugin name/index and configurable Pod annotations; webhook templates are inactive `.bak` files. | Add supported admission resources, a separate admission Deployment, chart validation, and readiness configuration. Do not treat backup templates as an existing security boundary. |
| `hack/common.sh`, `hack/deploy-virtual-k8s-cluster.sh`, `hack/virtual-cluster-redeploy.sh` | Runtime operations assume CRI-O. | Add shared runtime configuration, installation, service, socket, and qualification adapters. |
| `.github/workflows/virtual-e2e-singlenode.yaml`, `.github/workflows/virtual-e2e-multinode.yaml` | Existing virtual workload jobs. | Add qualified CRI-O/containerd workload-enforcement jobs and selected node-profile recovery coverage. |
| `.github/actions/deploy-virtual-cluster/action.yml`, `.github/actions/run-e2e-workloads/action.yml`, `test/e2e` | Existing deployment and Ginkgo workload entry points. | Extend these paths with runtime inputs, direct CRI tests, fault control, and retained evidence. |
| `test/e2e/demo_extended_resource_test.go`, `demo/extended-resource` | Already exercise custom DRA aliases without explicit Pod claim references. | Enroll their aliases and add automatic-prefix and mutation/race coverage. |

## Configuration and runtime capabilities

Introduce `--nri-enforcement-profile=off|workload|node` with default `workload`, environment variable `NRI_ENFORCEMENT_PROFILE`, and shared chart values:

```yaml
nriEnforcement:
  profile: workload
  tolerateMissingAnnotation: tolerate-missing-nri-plugins.noderesource.dev
  customDRAExtendedResources: []
  admission:
    replicas: 2
    tlsSecretName: ""
  bootstrapWorkloads: []
```

A non-off profile requires the admission component, trusted policy configuration, and qualified runtime provisioning. Require a serving TLS Secret and documented rotation procedure for admission; the environment adapter supplies it before activating webhook configurations. A one-node test environment may use one replica. Production guidance uses independent replicas, disruption protection, resource requests, and a certificate-expiry alert. Admission runs on primary networking, has no DRA claims or custom DRA alias requests, and does not connect to NRI. Its bootstrap must remain independent of this driver.

Reuse `kubeletPlugin.nriPluginName` and `nriPluginIndex`; do not introduce competing identity settings in the chart. Derive `42-dra-driver-sriov` from default name `dra-driver-sriov` and index string `42`. Validate the index with NRI's two-digit rules and the name with upstream identity validation. Both bare and index-qualified names are valid NRI map aliases; index alone is not. Use the qualified name throughout generated admission and runtime settings. A name/index change requires coordinated policy, Pod, and runtime rollout. [NRI plugin identity](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/pkg/api/plugin.go), [validator map aliases](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/pkg/api/validate.go).

Use the built-in default validator. NRI, the validator, and external plugin connections must be enabled. Initial source-checked candidates are CRI-O `v1.36.0` with NRI `v0.11.0`, and containerd `v2.3.0` with NRI `v0.12.0`. These are candidate source tags, not minimum supported versions, certified builds, or OpenShift support claims. Pin exact package/build and bundled NRI revisions; distribution backports must be inspected. The driver's `go.mod` NRI version does not establish runtime capability. [CRI-O configuration](https://github.com/cri-o/cri-o/blob/v1.36.0/internal/config/nri/nri.go), [CRI-O dependencies](https://github.com/cri-o/cri-o/blob/v1.36.0/go.mod), [containerd configuration](https://github.com/containerd/containerd/blob/v2.3.0/internal/nri/config.go), [containerd dependencies](https://github.com/containerd/containerd/blob/v2.3.0/go.mod).

WORKLOAD configuration for a fresh CRI-O deployment:

```toml
[crio.nri]
enable_nri = true
nri_listen = "/var/run/nri/nri.sock"
nri_disable_connections = false

[crio.nri.default_validator]
nri_enable_default_validator = true
nri_validator_required_plugins = []
nri_validator_tolerate_missing_plugins_annotation = "tolerate-missing-nri-plugins.noderesource.dev"
```

WORKLOAD configuration for a fresh containerd deployment:

```toml
[plugins."io.containerd.nri.v1.nri"]
disable = false
socket_path = "/var/run/nri/nri.sock"
disable_connections = false

[plugins."io.containerd.nri.v1.nri".default_validator]
enable = true
required_plugins = []
tolerate_missing_plugins_annotation = "tolerate-missing-nri-plugins.noderesource.dev"
```

For NODE, append `"42-dra-driver-sriov"` to the appropriate global list. For WORKLOAD, this driver's identity must not be in that list. Preserve other administrators' required plugins and unrelated validator settings; the empty example lists do not authorize clearing an existing configuration. Existing global requirements for other plugins can still affect platform availability.

CRI-O requires its prefixed field names. Use a supported drop-in such as `/etc/crio/crio.conf.d/90-dra-nri.conf`; containerd requires an active configuration fragment or explicitly configured import. Inspect active service arguments, merge with registry/CRI settings, validate, restart through the node adapter, and inspect effective settings afterward. [CRI-O validator reference](https://github.com/cri-o/cri-o/blob/v1.36.0/docs/crio.conf.5.md#crionridefault_validator-table), [containerd NRI configuration](https://github.com/containerd/containerd/blob/v2.3.0/docs/NRI.md).

`tolerate-missing-nri-plugins.noderesource.dev` matches the upstream documented example; it is not a built-in default. An administrator may override it. The runtime accepts one toleration key, and an effective true value skips **all** required-plugin checks, including requirements from annotations. There is no selective per-plugin exemption. Other validator categories still run. Runtime settings, admission protection, and any bootstrap templates must use the same key. [Upstream toleration example](https://github.com/containerd/nri/blob/53b711125c22339d9ba367bce242d9f1dfc6b75a/docs/validation-plugins.md#tolerating-missing-plugins).

## Mandatory workload admission

### Complete selection without driver-ownership guesses

Implement a shared pure `RequiresNRIEnforcement(Pod, Policy)` classifier in a new `pkg/admission/nrienforcement` package. It selects a Pod if any of the following is true:

- `spec.resourceClaims` is nonempty, or any container declares `resources.claims`. Treat references to ResourceClaims and ResourceClaimTemplates identically. Do not wait for an object to exist or for allocation to complete.
- Any request or limit key in regular or init containers uses `deviceclass.resource.kubernetes.io/`, or appears in `customDRAExtendedResources`. Check every container resource field supported by the qualified Kubernetes API, including restartable init containers and any supported Pod-level or resize resource fields. Ephemeral-container updates remain subject to annotation validation even where their API disallows resource requests.
- The old or new Pod has this driver's protected required-plugin marker. Once selected, an update cannot remove protection by removing a request or switching a reference.

This covers generated claims, all `firstAvailable` alternatives, claim-name replacement, and claims which later allocate to a different driver without making API calls in admission. A reference to an unallocated or missing claim is still marked. The prefix covers any DeviceClass, including classes created later. Custom aliases require the provisioning contract below. Other explicit DRA users intentionally acquire a dependency on this plugin's connection; the NRI callback must allow allocations proved to belong only to other drivers. A narrower ownership policy requires a separate trustworthy class/driver contract and is not part of this implementation. [DRA and extended-resource behavior](https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/).

Do not scope selection by tenant labels, the Multus annotation, or a namespace exemption. Admission applies cluster-wide to the described resource shapes. The shared unified-mode policy uses the same candidate predicate in every namespace and prevents networks-annotation changes after scheduling. ResourceQuota presence or absence does not establish authorization or enrollment; there is no namespace opt-out from these protections.

### Mutation, final validation, and annotation precedence

The mutation result includes a whole-Pod requirement:

```yaml
metadata:
  annotations:
    required-plugins.noderesource.dev: '["42-dra-driver-sriov"]'
```

This bare required-plugin annotation is also the protected consumer marker used by the NRI callback fast path. No second marker annotation is needed. It means that admission selected the Pod, not that allocation necessarily belongs to this driver.

NRI resolves `<key>/container.<container-name>` before `<key>/pod` before bare `<key>`. For selected Pods, strictly parse every existing required-plugin value as a list of strings, preserve other plugins, add this driver's qualified identity, deduplicate, and emit canonical JSON. Always create the bare value and also merge the identity into every existing `/pod` and `/container.*` value, including names not yet present in the Pod. Reject malformed input instead of overwriting it. Use the same upstream-compatible interpretation in final validation; a substring test or a bare-key-only check is insufficient. [NRI annotation lookup](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/pkg/plugin/annotations.go).

Provide a mutating webhook with `failurePolicy: Fail`, `reinvocationPolicy: IfNeeded`, `sideEffects: None`, and a bounded timeout, plus a separate final validating webhook with `failurePolicy: Fail`. Handle Pod CREATE/UPDATE and supported annotation/resource mutation subresources, including `pods/ephemeralcontainers` and `pods/resize`. Do not intercept status-only requests that cannot change these inputs. The validator recomputes selection from the final old/new objects and requires the identity in the bare list and every scoped list. This catches another mutator dropping the requirement, an unavailable/skipped mutator, per-container override attempts, and later updates. Validation must fail on inconsistent policy versions rather than accept missing protection.

Only final Pods are the execution boundary. Controllers may submit templates without the annotation; their generated Pods are injected and validated. Examples should show the resulting annotation, and templates may include it for clarity, but template mutation is not required to cover arbitrary controllers. Test direct Pods, Deployments, Jobs, DaemonSets, and generated ResourceClaimTemplate references. Do not trust owner references as authorization.

Use generated CEL `matchConditions` in both webhook configurations to select the same resource shapes and old/new marker-bearing requests before a network callback is attempted. Embed the immutable alias inventory and identity from the same versioned policy used by the handlers. Guard optional fields, and include old objects so removal cannot escape validation. A malformed marker or scoped requirement routes to validation and cannot take an unrelated-Pod bypass. The final validator must match final mutations even when the mutator did not initially match. Unit-test the CEL and Go classifiers against the same cases, and integration-test webhook ordering. Unrelated Pods with no DRA requests and no relevant NRI annotations do not call these webhooks; loss of admission endpoints must not stop ordinary platform Pod creation. [Kubernetes webhook matching and reinvocation](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/).

Install a built-in `ValidatingAdmissionPolicy` and binding with denial actions to protect the configured toleration key in both profiles. Reject unapproved presence of the bare key, `/pod`, and `/container.*`, regardless of value. Protect malformed lookalike scoped forms too. A tenant must not gain a bypass via a later ephemeral container or by placing true on an overriding scope. In WORKLOAD, consuming/marked Pods have no exemption; administrator recovery Pods do not need one unless another existing global requirement does. In NODE, permit only the protected bootstrap inventory described below. Protect policy resources, webhook configuration, serving credentials, and alias inventory with RBAC.

### Custom aliases and admission rollout races

`customDRAExtendedResources` is an administrator-owned list of all custom DRA aliases in this hardened cluster, not a best-effort informer cache of aliases currently observed. For example, enroll `example.com/sriov-port1` and `example.com/sriov-port2` before applying the existing extended-resource demo. Store policy as an immutable, versioned ConfigMap rendered from chart values, with a hash shared by webhook configuration, admission Pods, node preflight, and test evidence. No new CRD is required.

Add a fail-closed DeviceClass admission handler for CREATE/UPDATE which rejects any nonempty `spec.extendedResourceName` absent from the active policy. Apply it to all DeviceClasses, regardless of configuration or selectors. This is an intentional cluster-level integration requirement because arbitrary CEL ownership cannot be safely inferred. Tenant users must not modify DeviceClasses or this inventory. Ordinary non-DRA extended resources remain unaffected unless administrators register their names as DRA aliases.

The first implementation supports append-only alias registration while enforcement is active. Do not hot-discover an alias after scheduling has started, or immediately forget an alias when a class is deleted. Adding an alias that was previously used by an unmarked Pending Pod could otherwise create an enforcement gap without any Pod update. Changing selectors or claim allocation also must not be used to convert an unprotected workload into a protected one after admission.

Use a provisioning operation for initial enablement and each alias addition:

1. Suspend affected workload rollout and scheduling on all nodes that can serve the alias; restrict DeviceClass writes to the provisioning principal. Install a temporary built-in admission deny rule for new uses of the affected alias while policy resources change.
2. Install the new immutable policy, all webhook match conditions/handlers, and DeviceClass registration checks. Verify convergence and denial/injection probes across the cluster's API-server instances; do not activate a class while any serving admission revision lacks the alias.
3. Inventory existing Pending and running Pods using the alias. Delete/recreate unprotected Pods and their sandboxes after the new admission is active; audit all generated/controller templates for continued use. Existing Pod annotation patches do not update a runtime's saved sandbox annotation snapshot.
4. Activate the DeviceClass alias only after old unprotected Pods are gone and the new policy passes probes. Remove the temporary deny rule, resume admission/scheduling and workload rollout, and verify generated claims plus runtime annotations.

For managed control planes where convergence cannot be directly inspected, keep affected nodes unschedulable and class activation blocked until provider-supported policy propagation verification and probes establish the same boundary. Do not claim a live, atomic multi-object Kubernetes configuration transaction. Removing an alias or bypassing this provisioning procedure is unsupported in the initial workload profile; drain and reprovision the profile for inventory reduction. Privileged administrators can disable admission or edit host runtime configuration, so they remain inside the trust boundary.

## Durable readiness and callback availability

In WORKLOAD, `pkg/driver/dra_hook.go` must verify the protected whole-Pod requirement and every effective scoped override before **every fresh or cached `NodePrepareResources` success**, before device mutations or returning prepared CDI IDs. Resolve the actual consuming Pod by UID using the shared resolver; require this driver's configured identity in the bare marker and all applicable scopes, and reject any effective missing-plugin toleration. A missing marker, changed claim/Pod identity, failed API read, or invalid override returns an actionable prepare error requiring corrected admission and Pod recreation. Do not add the marker from the driver and then assume an existing sandbox received it. Run this guard before early cached-result returns as well as before new preparation. This is defense against missed admission paths, class/alias drift, and claim-name replacement; it does not replace initial admission or the rollout of previously prepared kubelet/sandbox state.

The bbolt proposal owns the sole authoritative `claims` bucket, keyed by claim UID, with JSON claim records embedding prepared devices, lifecycle, ownership, attachment identity, and pending publication work. The unified-mode proposal owns UID-checked Pod/claim resolution and persisted Multus-versus-NRI ownership. Use those contracts for both `RunPodSandbox` and a read-only `CreateContainer` handler; do not add an authoritative in-memory prepared-device map.

After a successful store lookup, **no records and no protected requirement** means an unrelated-Pod no-op with no API lookup, but only when `workload` preflight has verified complete admission and annotation projection. Existing records or a protected requirement demand identity/state checks. A marked Pod with resolved allocations exclusively for other drivers may return success without this driver's attachment records. Unallocated/missing claims, changed claim UIDs, incomplete generated/extended-resource status, API failures during required resolution, malformed markers, and unknown ownership must fail safely. Store errors are never equivalent to an empty result. Off/node profiles cannot assume the workload marker is complete and ordinarily retain bounded claim resolution when records are absent. NODE has one explicit recovery fast path: after a successful empty store lookup, a Pod matching the locally configured, protected bootstrap inventory with an effective true toleration returns a no-op without an API read. Admission must have authorized that toleration and protected its identifying fields; static manifests require the equivalent trusted node-provisioning contract. Match runtime-visible namespace and protected bootstrap identity from the provisioned inventory, and never grant this path from an arbitrary label alone. Service-account and creator authorization is checked by admission before execution, not rediscovered through an API dependency during recovery. Existing managed records still require readiness checks and cannot take this path.

For managed records, acquire the shared Pod operation lock and require:

- Committed preparation for every device and completion of pending shared CDI/artifact changes. Inspect all sibling claim records, not only the first allocation.
- For each NRI-owned network allocation, committed `attached` state for the current Pod UID, claim incarnation, sandbox ID, node boot identity, and required synchronous metadata refresh.
- For device-only VFIO allocations, committed preparation and CDI artifacts without inventing CNI attachment records.
- For Multus-owned records, valid prepared ownership and artifacts, with no driver CNI ADD/DEL, host-network mutation, or fabricated NRI attachment result. Multus/runtime CNI owns network completion.

`CreateContainer` must not issue late ADD to repair missed sandbox networking. Reconcile with `Synchronize` before declaring readiness: a connection restored after a missed `RunPodSandbox` does not establish attachment success. Use supported inspection or safely recreate the sandbox; do not invent `attached` from plugin presence. Keep transactions short, with API/runtime/CNI calls outside transactions, and serialize readiness with prepare/teardown through the shared operation lock.

Open and validate the database before NRI registration. Share a process-state readiness object between recovery, NRI synchronization, and `pkg/driver/health.go`; expose gRPC `readiness` separately from existing liveness. Mark unready on disconnect and preserve the existing main-context cancellation/restart behavior. API trouble requiring workload retries must not cause a liveness restart loop.

WORKLOAD removes the missing-plugin dependency for ordinary platform Pods, and the local fast path avoids API lookups for them. It is not complete isolation from a connected NRI plugin: callbacks are runtime-wide, so callback stalls, database failures, or errors before classification can still delay/fail unrelated operations. Preserve bounded callbacks and test unrelated Pod startup with the driver's API access blocked. Do not advertise universal platform availability under arbitrary driver or storage failure.

## Runtime in-flight failure prerequisite

At the requested NRI revision, `Adaptation.CreateContainer` records a plugin in the validation map before calling it. Fatal RPC failures in `plugin.createContainer` close the connection and return `(nil, nil)`; removal is deferred until after the request. This creates a source-derived path where the same request can validate against a plugin whose readiness callback failed. It must be reproduced and closed before strict-profile qualification. [Map insertion and deferred cleanup](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/pkg/adaptation/adaptation.go#L301), [fatal callback handling](https://github.com/containerd/nri/blob/84180b63351d03b54a317a265ba01e3e9db19f24/pkg/adaptation/plugin.go#L735).

Implement or obtain an upstream/runtime fix that builds the final participant set after callback processing and excludes failed/closed participants at validation. Ordinary callback errors remain creation errors. Preserve registration-based presence for healthy plugins that do not subscribe to `CreateContainer`, and preserve optional-plugin and explicitly authorized toleration behavior. Do not solve this by making every optional plugin failure fatal without a separate compatibility decision.

Add adaptation tests plus a deterministic real-runtime test that pauses this driver's `CreateContainer` callback, disconnects/kills it or expires its RPC deadline, and verifies failure of that **same** request. A later request after plugin removal is insufficient. Review concurrent connection closure at the validation boundary; a successful callback followed by later disconnection does not revoke completed creation.

Inspect the actual NRI code bundled into each candidate runtime, not only the driver's library. Pin builds containing the accepted fix or a proven equivalent, record patch provenance, and block strict-profile release/documentation claims until both runtimes pass the test. Shipping configuration without that fix may provide disconnected-before-request protection, but does not satisfy this HLD's full acceptance contract. Upstream acceptance and downstream packaging are external prerequisites, not work this repository can assume complete.

## Bootstrap, OpenShift, and upgrade behavior

Under WORKLOAD, ordinary OpenShift platform services, the admission Deployment, and the driver need no missing-plugin toleration annotation when they have no selected DRA inputs and no other global NRI requirements apply. Kubernetes Service objects are irrelevant; runtime input comes from Pod/sandbox annotations. Confirm cold boot and recovery with the driver absent and admission endpoints unavailable: ordinary platform Pods must still be admitted and created, while selected new consumers must fail admission or runtime creation.

For OpenShift, document the supported node-configuration/MachineConfig mechanism for the exact tested release and worker pool. Do not advise editing operator-managed Pod templates throughout the platform or replacing vendor runtime binaries as the standard installation path. Inspect the installed CRI-O build and effective config and run qualification probes; an upstream source tag does not establish an OpenShift release's capability. If the bundled runtime lacks the required fix, report the strict profile unsupported until a supported runtime update is available.

NODE creates a bootstrap dependency. Before enabling its global list, inventory the driver, admission service, static control-plane Pods, and essential primary-network/API/recovery components that must start without it. Annotate their administrator-owned Pod templates or protected static manifests with the configured toleration key set to `"true"`. Do not exempt all of `kube-system` or all platform namespaces. The inventory identifies namespace, service account, workload kind/name, protected label, and allowed creator/controller principals; admission must match the protected tuple and principal, not trust a tenant-supplied owner reference or label. RBAC must prohibit tenants from creating or editing those templates, using those privileged service accounts in reserved namespaces, or modifying the exemption policy. Static manifests are protected by node filesystem ownership because their execution bypasses normal API admission.

Profile rollout is a provisioning operation:

1. Cordon affected nodes, freeze selected new workloads and DeviceClass alias changes, and qualify the runtime build and annotation projection. Install independent admission endpoints before fail-closed configurations.
2. Install the versioned selection/protection rules, shared unified annotation-immutability policy, and custom alias guard. For NODE, install and validate the bootstrap inventory before global enforcement.
3. Drain/recreate existing selected Pods and sandboxes; merely patching running Pods with the requirement is insufficient. Confirm no unprotected Pending consumer can be scheduled. Preserve durable device/attachment cleanup according to the bbolt/unified contracts.
4. Activate runtime configuration and the driver's matching profile, recover/synchronize state, and run required allow/deny plus unrelated-platform probes.
5. Resume selected workloads and uncordon only after both unified routes pass. Recheck after runtime restart and cold reboot.

Switching profiles or plugin identity requires the same audit and relevant Pod recreation. Rollback keeps admission/enforcement while restoring compatible software. Disabling enforcement is an explicit administrator policy change, not an automatic recovery action; it must not remove another plugin's configuration. An old binary that does not understand durable state requires the drain/cleanup downgrade procedure from the bbolt proposal.

## Implementation sequence

1. Add typed profile/identity/policy configuration and pure annotation/classification helpers in `pkg/admission/nrienforcement`; add CLI/env parsing in `cmd/dra-driver-sriov/main.go` and `pkg/types/config.go`. Keep one canonical identity and reject non-off with legacy `MULTUS` or incomplete admission settings.
2. Add a separate `cmd/nri-enforcement-admission` entry point with Pod mutate/validate and DeviceClass validate handlers. Add chart Deployment, Service, least-privilege RBAC, TLS Secret integration, immutable policy ConfigMap, webhook configurations, and built-in toleration-protection policy. Use final Pod admission as the boundary and share policy settings with unified annotation immutability. Do not revive inactive `.bak` resources without reviewing them.
3. Implement policy provisioning/preflight under `hack/`, including initial/alias freeze, version/hash agreement, existing Pod inventory, alias-registration denial probes, and runtime annotation projection verification. Emit actionable failures and retain the prior working policy until rollout completes.
4. Add the mandatory fresh/cached prepare marker guard in `pkg/driver/dra_hook.go`. Integrate bbolt and unified contracts into `pkg/nri`, add `Synchronize`/`CreateContainer`, and extend `pkg/driver/health.go` with shared readiness. Regenerate affected mocks and exercise callback behavior under real hook deadlines.
5. Add a common runtime helper under `hack/` for `CONTAINER_RUNTIME=crio|containerd`, exact `CONTAINER_RUNTIME_VERSION`, and `NRI_ENFORCEMENT_PROFILE`. Wire it into existing deployment/redeploy scripts and composite action inputs; select `UNIFIED` explicitly for this suite. Own installation, registry trust, CRI endpoint, drop-in/import validation, service restart, and diagnostics in this layer.
6. Complete upstream/runtime in-flight fixes and pin qualified builds. Add a node-side CRI fixture/helper and `test/e2e/runtime_nri_enforcement_test.go` using the existing Ginkgo framework and workload action. Extend both virtual-e2e workflows with runtime/profile jobs; retain unrelated-Pod and cold-boot recovery checks.
7. Update root/Helm README, values documentation, and a dedicated runtime-enforcement operator guide with exact tested versions, profile scope, admission enrollment, custom-alias provisioning, OpenShift installation boundaries, bootstrap, recovery, and effective-config/CRI diagnostic commands. Mark unsupported versions clearly.

Each step is a reviewable implementation change with validation. This design-only PR does not implement admission, provision runtime builds, or certify protection.

## Verification and acceptance evidence

Run `make check`, `make test`, focused affected package tests with `-race`, Helm lint/render assertions, and the existing `make e2e-workloads` entry point. Compile and exercise CEL policies against the qualified API server; mocked webhook handlers alone cannot validate admission matching, ordering, subresources, or propagation.

Add fresh/cached prepare tests proving missing or overridden markers reject before host changes/CDI return, including a replaced claim and a missed custom alias. Admission tests cover explicit claims, templates, missing/replaced claims, every `firstAvailable` branch, automatic prefix, registered custom aliases, mixed other-driver claims, malformed lists, all scoped required/toleration forms, removal/updates, init/sidecar/ephemeral containers, resource resize, controller-generated Pods, conflicting mutators, webhook outage, and alias activation races with old Pending Pods. A new custom alias must be denied before registration. Ordinary non-DRA and unregistered legacy device-plugin workloads remain unaffected. Test startup with admission endpoints down and the driver's API access blocked separately.

Runtime tests must demonstrate the annotation reaching the actual NRI `PodSandbox` input through kubelet and CRI, including effective container-name scopes; observing only the API Pod annotation is insufficient. If a runtime or distribution filters these keys, configure its supported forwarding mechanism in the adapter and qualify that setting, or fail preflight. Include a projection-negative control so dropped annotations cannot produce a false successful-enforcement result.

### Direct CRI proof

Use pre-pulled images and a node-side helper over an administrative connection. Create a real, non-DRA Kubernetes fixture with the required annotation, mirror its UID/annotations in a CRI sandbox, and let the connected driver's resolver prove no owned allocation. This isolates required-plugin validation from scheduling, kubelet DRA registration, CDI, and CNI allocation failures.

1. With the driver connected, create/start a uniquely identified container and verify its execution marker. Delete the container and retain the sandbox.
2. Hold the driver down deterministically by controlling restart or its NRI connection; confirm runtime-observed disconnection. Deleting a DaemonSet Pod and racing replacement is insufficient.
3. Create a new container in that sandbox. Require a failed CRI RPC, validator evidence naming the required identity, and no execution marker or started container.
4. Restore the driver, verify synchronization/readiness, retry, and require successful execution. Always restore fault controls and clean resources after failures.
5. On the disposable node, use a wrong identity as a denial control, and remove the fixture's requirement or disable the validator as an allow control. The equivalent non-DRA probe must then succeed with the driver absent. Restore qualified configuration immediately afterward.

Run real Kubernetes consumer Pods through both unified routes as well. Prepare a sandbox/claim before disconnecting and cause another container creation so the test reaches CRI even while kubelet DRA preparation might otherwise fail earlier. Add a newly scheduled consumer test for user-visible outage behavior, but do not count a Pending Pod as direct validator evidence.

| Scenario | Required result |
| --- | --- |
| Driver never connected or held disconnected | Selected fresh container creation fails with validator evidence; unrelated unmarked WORKLOAD container creation succeeds. |
| Disconnect/timeout during readiness callback | The same in-flight creation fails on each qualified runtime build. |
| Missed sandbox hook, then reconnect | Presence alone cannot pass; NRI-owned consumers require verified attachment or safe sandbox recreation. |
| Driver restart with active consumers | Durable identities survive, no duplicate ADD, no teardown of running attachments. |
| Multus annotation present | Selected Pod still requires plugin presence; this driver performs zero CNI ADD/DEL from NRI callbacks. |
| Store error, stale sandbox, pending CDI/metadata | Managed creation fails; no empty-state or registration-only success. |
| Other DRA driver only | Conservative marker requires plugin presence; connected handler permits a definitively resolved other-driver allocation. |
| Unrelated platform with API blocked for driver | Local unmarked/empty-record callback path does not read the API; ordinary Pod starts. |
| Admission endpoints unavailable | Selected admission fails; ordinary unmarked/non-DRA Pod admission still succeeds. |
| Runtime restart and cold reboot | Effective config and annotation forwarding survive; profile-specific recovery works without a bootstrap cycle. Approved NODE bootstrap with an empty store takes its protected local no-op even while the API is unavailable; forged bootstrap identity is rejected. |
| Wrong identity, ignored drop-in, unsupported validator | Preflight fails and no supported-enforcement result is reported. |
| Base, Pod, container-scoped bypass attempts | Final admission rejects/remediates dropped requirements and denies unapproved tolerations; later ephemeral creation cannot bypass. |
| Alias added while old unmarked Pod is Pending | Provisioning prevents class activation/scheduling until policy converges and the Pod is recreated. |
| Start after earlier successful creation | Documented creation/start boundary holds; no claim of retroactive revocation. |

CI artifacts include exact runtime/package/NRI and patch revisions, Kubernetes/Multus/kernel versions, policy hashes, sanitized effective config, rendered admission/driver/bootstrap manifests, observed runtime annotations, Pod/claim/sandbox/container identities, timestamped RPC results, runtime/driver logs, events, execution markers, and durable attachment diagnostics. Retain evidence before VM cleanup. Assertions must use bounded waits, verify nonzero executed cases, and fail on skipped qualification prerequisites.

Completion requires both runtimes to deny selected creation while disconnected, reject same-request callback failure, reject missing managed attachment after reconnect, recover both unified routes, and preserve ordinary WORKLOAD platform startup without adding exemptions. Alias/admission completeness and runtime annotation projection are part of that proof. Unqualified runtime fixes or missing admission are release blockers, not undocumented follow-up work.

## Alternatives and tradeoffs

Node-wide enforcement alone is simpler but requires recovery annotations on platform workloads that must start during a driver outage. An optional tenant-supplied requirement is easy to omit. Class-name lookup during Pod admission misses claim replacement and generated/allocation changes, while inspecting CEL selectors cannot establish driver ownership. A purely asynchronous alias informer introduces a gap between class activation and Pod admission. These alternatives do not satisfy the selected workload profile.

The chosen profile adds an independent admission service and a guarded custom-alias lifecycle, and conservatively couples other explicit DRA users to this plugin. It avoids editing ordinary OpenShift platform Pod templates and keeps their admission independent of webhook availability. Runtime-wide callback and storage failures still have the limits described above. Narrowing conservative DRA scope, supporting live alias removal, and stronger start-time revocation are future proposals rather than undecided implementation details.
