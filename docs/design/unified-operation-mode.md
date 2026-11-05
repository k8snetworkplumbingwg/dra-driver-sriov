# HLD: Unified networking operation mode

Status: proposed. This document specifies the implementation and acceptance contract; it does not claim that the feature or its execution tests already exist.

Implementation tracking: [#162](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/162). Merging this HLD does not complete the implementation issue.

Source baseline: repository `main` at `dc62c4c89220ea6f5ed104634d5c1b78447216f6`. File names and current behavior below refer to that revision.

## Objective and decisions

Run one driver DaemonSet that supports Multus and NRI networking on the same node. For each Pod, the presence of `k8s.v1.cni.cncf.io/networks` selects Multus; its absence selects NRI. The selection applies to every network device owned by this driver in that Pod. There is no per-device fallback between attachment owners.

“No NRI call” for a Multus Pod means that the driver never invokes its CNI ADD/DEL path or changes that Pod's networking from an NRI callback. The runtime may still deliver callbacks to the registered plugin. Registration must remain active so unannotated Pods work and the required-plugin enforcement in `runtime-nri-enforcement` plan can verify the connection. DRA prepare/unprepare still manages host preparation, CDI, and Multus device-info artifacts.

This is an operation mode of the existing binary, not a new Kubernetes operator. The current public setting is `kubeletPlugin.configurationMode`, with CLI flag `--configuration-mode` and environment variable `CONFIGURATION_MODE`.

## Scope and prerequisites

This feature includes Pod resolution, deterministic attachment ownership, prepare/unprepare integration, NRI callback routing, deployment mounts and RBAC, annotation-update admission, migration instructions, and end-to-end evidence for both routes on one node. The DeviceClass-only configuration proposal supplies trusted effective configuration and the NRI NAD trust boundary. This proposal supplies the per-class Multus NAD authorization contract below. The bbolt proposal supplies transactional claim records and recoverable lifecycle operations. Implement those shared contracts first; do not introduce a second store or an additional checkpoint for this feature.

Runtime required-plugin enforcement is specified separately. Integrate the workload-scoped profile proposed there, including its admission-protected required-plugin marker, without introducing a node-wide requirement or blanket platform namespace exemptions. The `CreateContainer` readiness contract also works with an explicitly selected node-wide profile. The runtime proposal owns missing-plugin admission, bootstrap rules, runtime version qualification, and runtime configuration.

Use its shared `--nri-enforcement-profile=off|workload|node` / `NRI_ENFORCEMENT_PROFILE` setting and Helm `nriEnforcement.profile`; do not add a second enforcement selector. Existing installations retain `off` by default for upgrade compatibility. Qualified unified environment jobs explicitly select `workload` after its admission preflight succeeds.

Non-goals are a new operator controller, mixed Multus and NRI attachment ownership within one Pod, concurrent sharing of a VF, live conversion of running sandboxes between owners, implementing Multus networking inside the driver, and reporting Multus CNI results through fabricated NRI status. Removing the legacy mode settings is a subsequent breaking change.

## Current implementation

| Area | Current behavior | Required change |
| --- | --- | --- |
| `cmd/dra-driver-sriov/main.go` | Starts NRI unless the global mode is `MULTUS`. | Start NRI in unified mode regardless of individual Pod annotations. |
| `pkg/devicestate/state.go` | Global mode determines NAD lookup, interface names, and device-info attributes during prepare. | Pass an explicit, persisted Pod attachment owner into preparation. |
| `pkg/devicestate/deviceinfo.go` | Global mode controls both device-info creation and removal. | Use recorded ownership and artifact identity, including during restart cleanup. |
| `pkg/driver/dra_hook.go` | Associates a claim with its sole `status.reservedFor` entry but does not fetch the Pod. Batch completion selects the first Pod UID for global CDI generation. | Resolve and validate the Pod, group work by Pod UID, and pin routing before side effects. |
| `pkg/nri/nri.go` | Uses prepared NAD configuration to decide whether to attach/detach. Successful ADD state is persisted asynchronously. Missing devices or network namespace may return success. | Gate networking on durable ownership; commit attachment state synchronously; distinguish unrelated Pods from unavailable required state. |
| `pkg/podmanager/podmanager.go` | Stores mutable prepared-device pointers and a checkpoint. `DeleteClaim` deletes the entire Pod entry. | Use the bbolt store contract and preserve sibling claims. |
| Helm DaemonSet | Mounts either the NRI socket or Multus device-info directory according to the global mode. | Unified deployments mount both, plus existing CNI binaries, CNI cache, netns, CDI, and kubelet plugin data. |
| Helm RBAC | Allows NAD reads but has no Pod read permission. | Add `get` on Pods; add list/watch only if an informer is actually introduced. |

Multus matches a NAD's `k8s.v1.cni.cncf.io/resourceName` to allocated device attribute `k8s.cni.cncf.io/resourceName`, then supplies `k8s.cni.cncf.io/deviceID` to the delegate CNI. These are distinct annotation and attribute keys. Pin the tested Multus build because its DRA behavior is an external dependency. [Multus DRA integration](https://github.com/k8snetworkplumbingwg/multus-cni/blob/master/docs/how-to-use.md).

## Annotation contract

Use the existing `NetworkAttachmentAnnot` constant and the NAD client parser for the supported text/JSON network-selection forms. Never use `network-status`, a substring match, or a nonempty-string test to decide annotation presence. [NAD annotation definition](https://github.com/k8snetworkplumbingwg/network-attachment-definition-client/blob/master/pkg/apis/k8s.cni.cncf.io/v1/types.go).

| Pod input | Selected owner | Behavior for this driver's claims |
| --- | --- | --- |
| Annotation key absent | `nri` | Resolve the admin-configured NAD and use NRI CNI ADD/DEL. Preserve the explicit VFIO-without-NAD path. |
| Key present with valid text or JSON selections | `multus` | Validate matching Multus device attributes and selections; prepare device-info artifacts; NRI networking is a no-op. |
| Key present with empty string, whitespace, `[]`, or malformed content | `multus`, invalid request | Return a clear prepare error for a consuming Pod. Do not reinterpret as NRI. |
| Key present but selections include only unrelated networks | `multus` | Do not run NRI. Fail preparation if a driver-owned network attachment has no matching selection. An explicitly device-only VFIO allocation needs no network selection. |
| `network-status` present but `networks` absent | `nri` | Status is output, not the routing input. |
| No claims allocated by this driver | Not managed | Do not reject or configure the Pod merely because its Multus annotation is invalid. |

Validate that every driver-owned device requiring network attachment has nonempty string `resourceName` and `deviceID` attributes and a selected, permitted NAD with the corresponding resource name. Account for repeated selections and multiple allocations; a single selection must not silently stand in for multiple required attachments. Extra non-SR-IOV Multus networks remain allowed. Reuse the pinned Multus matching contract rather than inventing a different device ordering. Do not implement arbitrary mixed Multus/NRI devices within one Pod in this change.

Require admin-controlled NADs and the per-class Multus allowlist below, in addition to the trusted configuration supplied by the [DeviceClass-only configuration proposal](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/160). A matching resource name establishes device compatibility, not authorization to use that NAD. Annotation presence chooses an attachment owner; it does not authorize VF driver changes, trust settings, CDI edits, or a different device. An unrelated annotation must not become a way to launch a Pod with a silently unattached kernel device.

For allocations that require a kernel network attachment, reject `hostNetwork: true` before host changes. Attaching these devices into the host namespace is outside the supported contract. An explicitly device-only VFIO allocation may use host networking because it requires preparation/CDI rather than a CNI attachment. The same distinction controls whether missing netns or missing CNI configuration is an error; an empty NAD field alone must not imply a device-only allocation.

For the annotation snapshot, persist presence separately from a SHA-256 digest of the exact UTF-8 annotation value. Absence, an empty value, and JSON `[]` are distinct inputs. Parsing is used for validation and matching, not to reinterpret presence or permit changes to the original value after scheduling.

## Administrator-owned Multus network policy

Add Helm `kubeletPlugin.multusNetworkPolicy`, an exact DeviceClass-name map to permitted NAD namespace/name pairs. Its default is an empty map. For example:

```yaml
kubeletPlugin:
  multusNetworkPolicy:
    sriov-blue:
    - namespace: sriov-system
      name: blue-net-v1
    sriov-red:
    - namespace: sriov-system
      name: red-net-v1
```

Render this map into a read-only, administrator-owned ConfigMap mounted as `/etc/dra-driver-sriov/multus-network-policy.yaml`. Add `--multus-network-policy-file` and `MULTUS_NETWORK_POLICY_FILE` for deployments outside Helm. The file contains this class-to-NAD map; neither class nor NAD entries support wildcards, and namespaces must be explicit. Parse once during startup, reject malformed/duplicate entries, and log its canonical content digest. A missing file or empty map supplies no Multus network grants. Chart policy changes update a Pod-template checksum and roll the driver; no hot-reload controller or additional CRD is introduced. Tenants must not have write access to the policy ConfigMap or driver deployment. This policy is administrator deployment configuration, not `VfConfig` supplied by a claim or its template.

For a Pod consuming this driver's allocations, resolve each Multus selection with the NAD parser. An omitted namespace resolves to the Pod namespace; it never defaults to the driver's or the administrator's namespace. Fetch the exact NAD and compare its `k8s.v1.cni.cncf.io/resourceName` against all this driver's allocation results consumed by that Pod, including sibling claims outside the current RPC. Resolve each allocation's DeviceClass from its selected request/subrequest using the same allocation/configuration provenance rules as prepare. For every selection whose resource name can consume those allocations, the NAD namespace/name must be allowed for **every matching allocation's class**. This conservative check avoids authorizing an unintended class through Multus ordering when classes share a resource name. Administrators can use distinct resource names when different classes require different NAD permissions.

Complete these checks, the network-selection cardinality checks, and claim/class association resolution before writing device-info files, binding devices, or publishing prepared success. Missing class entries, denied NAD pairs, unknown class associations, and unreadable relevant NADs reject the managed attachment; there is no NRI fallback. A new prepare and a cached-result prepare both validate the currently loaded policy. A class entry cannot be bypassed by replacing an allowed NAD with a same-resource-name NAD in the tenant namespace. An NRI-owned Pod does not consult this Multus allowlist; its NAD comes from administrator `VfConfig`. Explicit device-only VFIO allocations require no Multus NAD grant unless they are actually selected for a managed CNI attachment.

Selections whose resource names cannot consume any of this driver's allocations remain outside this policy. This check grants no permission to unrelated bridge/macvlan/other-driver networks; their owners and cluster network admission remain responsible for them. Conversely, a selection matching this driver's allocated resource name is managed even if the tenant calls it an unrelated network. This feature does not build a general Multus admission controller.

Allowlisted NADs reside in namespaces where tenants cannot create, update, delete, or replace them. Administrators use versioned NAD names for configuration changes and retain old NADs until affected sandboxes complete Multus teardown. Record the selected NAD namespace/name/UID, CNI-config digest, class identity, and applied policy digest in each claim's existing JSON record before preparation succeeds. Repeated preparation verifies current authorization and rejects a changed/replaced NAD under an already prepared identity; it does not reinterpret old artifacts. Cleanup always uses recorded ownership and artifact identities even after a policy rule is removed, and never requires renewed authorization. Multus still owns CNI DEL: preserving the snapshot supports verification and recovery, not a new driver-owned Multus CNI path. Revoking a policy blocks subsequent prepare authorization; it does not automatically detach running Pods.

## Component boundaries

Introduce shared `AttachmentOwner` and `PodAttachmentPlan` value types in `pkg/types`. The plan contains the UID-checked Pod identity, owner, annotation presence/digest, validated network selections, and stable device-to-interface assignments. Keep the user-facing mode enum in `pkg/consts`; it controls initial plan construction and process startup, not teardown ownership.

Add a Pod resolver in `pkg/driver` with an injectable interface for fake-client tests. Separate its API lookup from a pure owner-selection function. The resolver verifies direct claims, `ResourceClaimTemplate` generated claims through Pod status, and supported extended-resource claim mappings using the Kubernetes helpers available at the pinned dependency version. It returns a typed error for unresolved/unsupported references rather than inferring membership from name similarity. `pkg/nri` reuses the same identity and consumer-resolution rules through a shared interface; it must not implement a second, weaker claim-matching algorithm.

Refactor `PrepareDevicesForClaim`, `prepareDevices`, and `applyConfigOnDevice` to take an explicit preparation plan. In unified mode, replace `isStandaloneMode`/`isMultusMode` decisions with the plan owner. `Unprepare` and device-info cleanup take the recorded claim state, including the owner and exact artifact identities. Existing legacy modes construct fixed-owner plans at the boundary; they do not leak the current process mode into recovery.

The store owns `BeginPrepare`, claim/Pod snapshot reads, attachment transition updates, and claim deletion. The driver owns per-Pod serialization and preparation orchestration; device state owns host/CDI/device-info operations; NRI owns NRI CNI ADD/DEL orchestration. Reuse one per-Pod operation coordinator across driver and NRI paths, acquire it before store transactions, and never wait for it from inside a transaction. Use request context deadlines for locks and external calls. Store callbacks must not call the API server, CNI, or another component while holding a bbolt transaction.

Interface allocation first reserves names from committed sibling claims, then assigns each new NRI device the smallest unused `<defaultInterfacePrefix><index>` in deterministic allocation order. Preserve committed assignments when a later claim is prepared. Reject collisions between explicit names, automatic names, and known Pod interfaces before ADD. Multus interface names follow its validated network-selection contract and must satisfy the administrator's network policy; the driver does not allocate a second NRI name for the same device.

## Resolve the Pod before preparation

1. Inspect each claim independently. Require an allocation owned by this node and the existing supported single Pod reservation; validate reservation resource/group and namespace/name/UID. Continue to reject concurrent multi-Pod sharing of a VF.
2. Fetch the Pod through the API using the reservation name and claim namespace. Check the fetched UID against the reservation, check `spec.nodeName` against the driver's node, and verify that the Pod consumes the claim, including generated-claim and supported extended-resource mappings. Never attach state to a replacement Pod with the same name.
3. For an initial preparation, a missing Pod, authorization failure, UID mismatch, or unavailable API is a preparation error before any device mutation. The driver must not assume that unavailable metadata means an absent annotation.
4. Resolve and validate annotation semantics and trusted configuration. Serialize preparation for the Pod, allocate interface names across all its claims, then call the bbolt store's `BeginPrepare` to pin ownership with a revision check before host changes.
5. A repeated prepare first revalidates configuration provenance and allocation identity under the DeviceClass-only configuration policy, then reads existing durable state and returns the same prepared identity. Persistence never exempts a claim from current configuration-source checks. It does not switch owners based on a new process flag, missing callback annotations, or a changed live annotation. A newly added claim for the same Pod must match the pinned route and annotation snapshot.
6. Group all batch-level CDI work by Pod UID. An RPC containing claims for several Pods must produce separate global CDI files, interface-name assignments, and rollback scopes. A failure for one Pod must not remove another Pod's state.

The initial authoritative API lookup is needed because DRA preparation precedes the NRI sandbox callback and Multus needs device-info files during CNI setup. Deciding only in `RunPodSandbox` is too late.

## Durable ownership and lifecycle

Use the single `claims` bucket and versioned JSON records from the [bbolt plan](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/161). Reuse the existing `PreparedDevices` payload. Pod lookup scans claim records by `podUID`; no separate Pod, attachment, or index bucket and no authoritative in-memory prepared-device map are needed.

| Location within claim JSON | Required fields for this design |
| --- | --- |
| Claim wrapper at key `<claimUID>` | Version, Pod UID and namespace/name, `attachmentOwner` (`nri` or `multus`), annotation presence/digest, class-to-NAD authorization snapshot and policy digest, preparation phase and revision. |
| Existing `devices` payload and operation progress | Approved effective configuration, frozen NRI NAD configuration where applicable, prepared devices, original host driver, stable interface assignments, and pending shared-artifact work. |
| Embedded `attachments` | Sandbox ID, claim incarnation, pool/device identity, owner, `add-pending` / `attached` / `delete-pending` / `detached`, frozen CNI input/result, interface name, netns identity and node boot ID. Extend the identity with share ID if that feature is introduced. |
| Embedded `pendingUpdates` | Desired status publications or metadata refreshes, target UID, operation identity, generation, and retry progress. |

Store reads return decoded snapshots and errors. NRI updates reread the latest claim JSON, check identifiers/revisions, modify only the intended fields, and write within one transaction. All sibling claims must agree on routing and annotation snapshot; validate that agreement when inserting a claim. Pod-level synchronization serializes routing, shared CDI, and external device operations; bbolt transactions remain short and contain no API or CNI calls. Derive shared Pod CDI from sibling claim records, with pending artifact work retained in the claim performing the change until recovery can finish it.

For `nri`, prepare resolves the approved NAD name/namespace, snapshots its CNI configuration, and assigns interface names. For `multus`, prepare writes eligible device-info files before acknowledging preparation and leaves driver-owned NRI CNI configuration unused. Missing Multus attributes for a network allocation become a validation error, replacing today's silent skip. Both routes retain applicable host preparation and CDI behavior, including VFIO/RDMA support.

Before NRI ADD, commit `add-pending` in the owning claim's embedded attachment record. After ADD, persist the returned result and `attached` state before reporting hook success; Kubernetes status publication may remain asynchronous through embedded `pendingUpdates`. Commit failure after an external success is an uncertain operation requiring reconciliation, not proof that ADD never happened. Preserve the existing synchronous request-metadata refresh for NRI and test its failure path. Multus dynamic network metadata remains unavailable through this driver's NRI path; do not write empty NRI results over Multus-owned status or claim unsupported feature parity.

On stop, load the persisted owner and sandbox attachment identity. A `multus` record causes no driver CNI DEL, even if annotations were removed or the Pod API object no longer exists. An `nri` record cleans only its recorded sandbox attachments, even if an annotation was subsequently added. A delayed old-sandbox callback must not detach a replacement sandbox. Missing netns paths use the existing CNI DEL recovery behavior and persisted inputs. Keep failed cleanup records for retry.

Unprepare removes claim artifacts using their recorded ownership; it must not consult the current global mode. Coordinate attachment cleanup before restoring host drivers. Rebuild shared Pod CDI from surviving sibling claims, then delete only the requested claim key. Retain the last claim record and its embedded routing/attachment state until final global-artifact cleanup succeeds. Sequential reuse of a released claim by a new Pod gets a new route; simultaneous sharing remains unsupported.

## Restarts, annotation changes, and runtime admission

On restart, open and validate bbolt, recover prepared claim/artifact records, and reconcile attachment records before declaring the networking path ready. Implement NRI `Synchronize` or an equivalent verified runtime inventory step to associate running sandboxes with persisted state. Do not issue another ADD for a confirmed existing attachment. A node reboot, stale namespace, unknown legacy owner, or incomplete external operation needs the recovery procedure from the bbolt proposal. Do not infer old ownership solely from current Pod annotations.

Pod annotations can change. Persisting the decision protects driver retries and DEL, but Multus independently reads the live Pod. To close that race in the supported hardened deployment, admission must prevent changes to the networks annotation's presence or value once either the old or new Pod has a nonempty `spec.nodeName`. Apply this rule cluster-wide to candidate workloads selected by the shared runtime-admission predicate, including privileged workloads; handle Pod binding in the admission integration tests. There is no tenant-controlled namespace opt-out. Before scheduling, users may correct annotations; after scheduling, they recreate the Pod. This is part of the deployment contract, not a claim that a driver-local digest makes a mutable annotation immutable.

For initial ADD, compare any runtime-supplied annotation snapshot with the persisted digest; on disagreement, fail without attachment or fallback. Callback annotation omission is not evidence of absence. Use a UID-checked API read when additional initial-start validation is necessary, and fail safely if it cannot resolve an inconsistency. Stop/recovery uses durable state without depending on a surviving API object.

Keep NRI registered for the whole unified process. A Multus callback may read ownership and log the decision, but it must not perform CNI, host-device, device-info, or network-status mutations. A missing prepared-state lookup must distinguish absence from database failure. Under the verified workload-scoped runtime profile, admission guarantees that every consumer, including generated/extended-resource consumers, carries the protected bare `required-plugins.noderesource.dev` annotation containing the qualified plugin identity, by default `42-dra-driver-sriov`. Admission conservatively marks other explicit DRA consumers as well; the marker indicates required verification, not proof that this driver owns a device. If a successful store lookup finds no records and that protected requirement is absent, return an unrelated-Pod no-op without an API call. Existing records or the requirement demand identity/state checks; an unresolved consumer must not receive successful networking. The runtime proposal defines protection against per-container overrides, custom extended-resource alias registration, startup preflight, and migration of existing Pods. Do not enable this fast path without those guarantees.

For legacy or unenforced deployments without that admission guarantee, a new sandbox with no record requires bounded claim resolution before it can be classified as unrelated. An API failure remains an error in that profile. Workload-scoped validator configuration alone cannot prevent disruption caused by a connected plugin returning errors for unrelated callbacks, which is why the protected-marker fast path and its completeness tests are part of integration. A database read error is never treated as an empty successful result.

The default validator in the runtime proposal checks required-plugin participation at container creation. That does not prove the sandbox previously received successful NRI networking. Implement the shared `CreateContainer` readiness check: for every NRI-owned network allocation, require a committed attachment for the current sandbox and node boot identity, plus completion of required synchronous metadata refresh. Device-only allocations require committed preparation, without invented CNI attachment records. A failed check rejects container creation; it does not execute late ADD from `CreateContainer`. Multus ownership never triggers replacement NRI attachment through this gate. Subscribe to `CreateContainer` for this check as well as runtime validator participation, and cover init, application, and later-created containers.

## Configuration and migration

Add `UNIFIED` to `ConfigurationMode` and support exactly the existing case-sensitive values `STANDALONE`, `MULTUS`, and new `UNIFIED`. An omitted CLI flag, empty normalized internal setting, and the chart's default remain `STANDALONE` in this implementation. This avoids silently changing the owner of existing annotated standalone workloads. Explicit legacy settings retain their existing routing behavior; new mixed-mode examples and unified environment jobs select `UNIFIED`. Reject all other values before driver registration, and make Helm fail during rendering rather than deploy an invalid mode. Log a deprecation warning for legacy modes. Changing the default or removing the selector requires a separate breaking-change proposal and release note; it is not an implementation dependency or an unspecified step in this feature.

Unified Helm rendering mounts both `/var/run/nri/nri.sock` and `/var/run/k8s.cni.cncf.io/devinfo`. Document that a former Multus-only node must now provide NRI even if its current workloads are all annotated. Validate supported mode values in CLI and chart tests. Runtime-enforcement deployment must reject an incompatible legacy `MULTUS` configuration rather than claiming the required NRI plugin is available.

Rollout sequence:

1. Install the admin-only configuration and NRI NAD restrictions from the DeviceClass-only configuration proposal. Populate the per-class Multus NAD policy and protect/version its NADs before enabling unified Multus workloads. Provide separate approved examples for Multus, kernel NRI, and device-only VFIO.
2. Roll out the bbolt migration from the bbolt proposal while retaining each node's explicit old mode. Import a known old attachment owner; ambiguous legacy state requires draining the affected workloads rather than guessing.
3. Install required runtime capabilities, pinned Multus DRA support where used, and annotation-update admission. Stage runtime enforcement according to its workload-scoped rollout, including protected requirement injection/validation and migration of existing consumer Pods. If an administrator explicitly selects a node-wide profile, prepare its bootstrap exceptions before enabling it.
4. Cordon and drain an affected node before switching its mode, verify no live legacy attachments remain, change its DaemonSet configuration to `UNIFIED`, and verify both routes before uncordoning. Do not switch live annotated standalone Pods to Multus during upgrade.
5. Enable the hardened runtime profile after its bootstrap/recovery checks pass; run the driver-disconnect acceptance test on both runtimes.

Old binaries do not understand the unified ownership schema. Downgrade requires draining, successful cleanup with the new binary, confirmation that no claims/attachments remain, stopping the driver, and archiving the database before installing the old release. The bbolt proposal does not provide a live state export tool. Replacing a binary alone or copying back the deleted checkpoint is not a supported rollback.

Update `README.md`, the Helm README and values, `demo/multus-integration-*`, standalone/VFIO examples, `hack/deploy-virtual-k8s-cluster.sh`, `hack/virtual-cluster-redeploy.sh`, `.github/actions/deploy-virtual-cluster/action.yml`, and both virtual-e2e workflows together. Preserve existing legacy environment defaults while adding explicit unified jobs. Document that adding any Multus networks annotation changes ownership for all this driver's network devices in the Pod.

## Deployment and admission contract

Add a chart-managed `ValidatingAdmissionPolicy` and binding for the networks annotation update rule. Use the runtime HLD's shared candidate predicate: explicit Pod DRA claims, automatic DeviceClass extended-resource names, administrator-registered custom aliases, or an existing protected requirement. Evaluate both old and new objects. Apply it in every namespace; labels and quota presence cannot exempt a candidate. Guard the validation with this object-local predicate so unrelated platform Pods require neither annotation nor network webhook. The policy matches Pod UPDATE and compares both old/new annotation presence and exact value whenever either old/new `spec.nodeName` is nonempty. Match only the relevant Pod resource; unrelated status updates that do not change the annotation remain allowed. A failed policy evaluation rejects the update. Existing Pods may retain their current annotation, but changing it requires recreation.

Provide a shared administrator-facing admission values block rather than deploying overlapping policies from different feature implementations. The runtime proposal supplies consumer admission; this feature supplies annotation immutability. Test the combined rendered deployment and real API behavior, including concurrent annotation edits and scheduler binding. Directly bound Pods must receive the same protection on subsequent updates. No webhook or API lookup is required for this old/new-object comparison. The supported hardened profile must install and verify the policy before enabling unified workloads.

The unified DaemonSet includes the NRI socket mount with hostPath type `Socket`, the Multus devinfo mount with type `DirectoryOrCreate`, and existing CNI/netns/CDI/kubelet state mounts. Add Pod `get` RBAC without unconditional Pod list/watch. A missing NRI socket or registration failure prevents unified readiness even on a node whose current workload happens to use only Multus. Recovery and store validation finish before networking readiness is reported; liveness must not repeatedly destroy an otherwise recoverable database or running workload.

Log the Pod UID, claim UID, selected owner, lifecycle operation, and bounded error reason at routing and failure boundaries. Do not log full opaque NAD configurations. Expose route-specific preparation/attachment failures through the existing metrics endpoint, with owner and stable reason labels rather than Pod/claim UID labels. Operators must be able to distinguish invalid annotation, unresolved Pod, rejected NAD selection, state conflict, NRI unavailable, and CNI failure without inferring success from a ready DaemonSet alone.

## Implementation sequence

1. Land the DeviceClass-only validation and bbolt store contracts. Add `pkg/types/attachment.go` and `pkg/driver/podresolver.go` for route types, pure annotation validation, and UID-checked consumer resolution. Extend `pkg/driver/dra_hook_test.go` or the existing driver suite with fake-client failures and the annotation truth table.
2. Integrate `BeginPrepare` and Pod-scan reads in `pkg/driver/dra_hook.go`. Fix mixed-Pod batch CDI generation, stable interface assignment, sibling-claim deletion, and final-artifact ownership. Regenerate affected mocks after interface changes. This step must retain the bbolt proposal's recovery guarantees under both legacy owners.
3. Refactor preparation and cleanup in `pkg/devicestate/state.go` and `pkg/devicestate/deviceinfo.go` to consume the explicit owner. Preserve host/CDI/VFIO/RDMA behavior, and reject invalid Multus network matches before device mutation. Keep the existing CNI implementation behind its interface for observable route tests.
4. Route run/stop and `CreateContainer` in `pkg/nri/nri.go` through the store and shared resolver, add sandbox identity and synchronous state commits, and implement `Synchronize` reconciliation. Integrate the runtime proposal's protected consumer marker and readiness gate without Multus networking side effects.
5. Add `ConfigurationModeUnified` in `pkg/consts/consts.go`, normalize configuration once before startup, and update `cmd/dra-driver-sriov/main.go`. Update chart values/help, `templates/dra-driver.yaml`, `templates/clusterrole.yaml`, the Multus policy ConfigMap/read-only mount/checksum, and new admission templates. Add startup parsing and per-class NAD validation before any prepare side effects. Add chart rendering assertions for mode validation, mounts, policy scope, and registration settings. Update documentation and administrator-owned examples.
6. Add `unified` to `.github/workflows/virtual-e2e-singlenode.yaml` and `virtual-e2e-multinode.yaml` and update `.github/actions/deploy-virtual-cluster/action.yml` plus environment scripts. In `test/e2e/framework/features.go`, recognize `UNIFIED` explicitly and allow both `SkipUnlessMultus` and `SkipUnlessStandalone` suites to execute in it. Preserve a distinct mixed-route test that runs both workloads simultaneously; two separate legacy jobs do not prove unified behavior.
7. Add `test/e2e/unified_mode_test.go` for concurrency, invalid annotations, restart, and cleanup. Run it through the existing `.github/actions/run-e2e-workloads/action.yml` and `make e2e-workloads` entry point. Integrate the runtime proposal's CRI-O/containerd matrix and outage cases rather than creating a second runtime installer or relying only on mocked callbacks.

Each step should be a reviewable implementation change with its own required tests. The feature is complete only after the integrated unified job passes; merging this document or supporting store refactors alone is not completion.

## Verification and acceptance

Unit and integration coverage must include:

- Every annotation-table row, supported text/JSON forms, namespace-qualified names, repeated NAD selections, matching resource-name cardinality, and extra unrelated networks.
- Multus policy tests for omitted namespace resolving to the Pod namespace, missing class rules, same-resource-name unapproved NADs, shared resource names across classes, sibling claims outside the RPC, deny-before-side-effects, malformed policy files, changed/replaced NADs, and repeated prepare after policy revocation. Verify unrelated network selections and NRI/device-only paths receive no accidental authorization from the policy; cleanup remains possible after revocation.
- Pod lookup missing/forbidden/timeout, replacement UID, wrong node, invalid reservations, generated and extended-resource claim resolution, and multi-Pod reservation rejection before mutations.
- Multiple claims in one Pod, multiple Pods in one prepare RPC, concurrent prepare calls, interface-name stability across separate RPCs, and unpreparing one claim while another remains.
- NRI and Multus Pods on one manager instance; Multus run/stop callbacks assert zero CNI ADD/DEL, host/network mutations, and NRI status updates, even with a nonempty legacy NAD field.
- Annotation addition/removal/value change after scheduling denied by admission; disagreement injected below admission fails ADD without fallback; persisted ownership still drives DEL after Pod deletion.
- Duplicate run/stop, sandbox replacement, delayed old-sandbox callbacks, missing netns, ADD/DEL failure, database commit failure, restart after each persisted phase, stale asynchronous updates, and node reboot.
- VFIO without NAD on each route, CDI device availability, static metadata, and the documented difference in dynamic network metadata.
- Helm rendering for the migration modes and unified mode, required mounts, Pod RBAC, unknown-mode rejection, and enforcement incompatibility checks.
- Protected-marker classification with direct, generated, and extended-resource consumers; unrelated unmarked Pods do not trigger API reads when the verified workload-scoped profile is active. A database error, malformed/conflicting protected requirement, or a marked consumer with missing state cannot take that fast path.
- Mode parsing through flag, environment, and chart inputs; lowercase/unknown values rejected; omitted settings retain `STANDALONE`; `UNIFIED` starts NRI and renders both mounts. Feature skip helpers must not silently skip either route in the unified CI job.

On both CRI-O and containerd, schedule annotated and unannotated SR-IOV workloads concurrently on the same node. Assert expected interface names, PCI identity, IPAM results/connectivity, no duplicate attachment, correct device-info ownership, and clean IPAM/device recovery after deletion. Delete one of a Pod's claims where the supported lifecycle permits and verify sibling state remains. Restart the driver while both kinds of workloads run, then recreate sandboxes and verify deterministic cleanup and reattachment.

The outage test from the runtime proposal must cover unannotated and annotated consuming Pods, plus a previously prepared claim/sandbox scenario that reaches container creation after NRI disconnect. Under the workload-scoped profile, assert denial for marked consumers and successful creation of unrelated unmarked Pods. Assert the configured validator denial and no init/application container start; a Pending Pod alone does not prove runtime enforcement. Recover the driver and verify eventual progress without manual state deletion. Record runtime, NRI, Multus, kernel, and Kubernetes versions with the results.

The mixed-route test must observe the CNI invocation boundary, not only final Pod readiness. Use the existing injected CNI interface in unit/integration tests to count driver ADD/DEL calls, and a controlled CNI wrapper or equivalent runtime trace in end-to-end tests to identify the caller and sandbox. Assert one driver attachment sequence for the NRI workload, none for the Multus workload, and no device being moved twice. On deletion, verify IPAM release, removal of the correct devinfo/CDI artifacts, and restoration of the original driver without removing sibling claim records.

Implementation validation runs `make check`, `make test`, focused affected package tests with `-race`, Helm lint/render assertions, and `make e2e-workloads` against each qualified unified runtime environment. Retain execution reports with nonzero executed cases from both route suites; a skipped Multus suite does not count as a pass. This design-only PR requires Markdown/link/content review, not SR-IOV hardware execution, and must report the distinction.

## Alternatives and tradeoffs

A decision only in `RunPodSandbox` is too late to prepare Multus device-info artifacts. Inferring ownership from a nonempty saved NAD or the current global mode makes restart cleanup unsafe. A per-device owner would permit ambiguous double attachment within one Pod and is deliberately excluded. Persisting only the selected owner without protecting annotation updates would let Multus observe a different route input than the driver.

The selected design adds one initial Pod lookup, claim-record scans, and admission requirements. Pod scans are acceptable for bounded node-local prepared claims and avoid a second durable index; measure callback latency and keep API retries within the configured NRI hook deadline. If those scans become a demonstrated bottleneck, optimize the transactional store separately without adding an authoritative prepared-device cache. Runtime required-plugin enforcement and protected admission must be qualified together; merely keeping the NRI socket connected is not the feature's security or readiness proof.

Acceptance requires one driver instance to serve both routes, zero driver-owned CNI calls for Multus Pods, durable ownership across failure/restart, and no silent success for a managed network device whose selected owner cannot attach it. Documentation-only planning does not satisfy these execution checks.
