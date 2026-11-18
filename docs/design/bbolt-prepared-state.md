# HLD: bbolt as the prepared-device source of truth

Status: proposed. This document defines the feature contract and implementation work; it does not enable or implement the feature.

Implementation tracking: [#161](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/issues/161). Merging this HLD does not complete the implementation issue.

Source baseline: repository `main` at `dc62c4c89220ea6f5ed104634d5c1b78447216f6`. Proposed APIs and files below are implementation requirements.

## Problem, scope, and non-goals

The current driver treats an in-memory map as prepared-device state and periodically serializes it to `checkpoint.json`. A failed checkpoint write can leave memory ahead of disk, and a crash between a host change and checkpoint creation can lose the information needed to restore a device. Concurrent callers also share mutable device pointers. The replacement must provide one durable authority for claim ownership and operation progress, with recoverable boundaries around host and network changes.

This feature covers the store, first-load checkpoint import, all driver/NRI callers, shared CDI cleanup, recovery workers, process lifecycle, operator guidance, and failure/restart tests. It preserves the existing `STANDALONE` and `MULTUS` deployment choices until the separate unified-mode feature is implemented. Both modes use the same database; Multus remains responsible for its own CNI operations.

Non-goals are a distributed database, cross-node claim failover, shared VFs or claims, a transaction spanning bbolt and CNI/sysfs/Kubernetes, live downgrade to the checkpoint format, and secondary indexes or a prepared-device memory cache. This proposal does not itself change accepted configuration sources, select unified routing, or install runtime required-plugin enforcement. It defines the stored identities and extension points those features need.

## Decision and invariants

Replace the JSON checkpoint and `PodManager.preparedClaimsByPodUID` with one node-local bbolt database containing one `claims` bucket. Each claim UID maps to one versioned JSON record that reuses the existing `types.PreparedDevices` payload and adds lifecycle state. Claim ownership, selected network flow, attachment progress, and pending status publication live inside those records. Callers receive decoded snapshots, not references into a shared prepared-device map. Temporary request objects, locks, discovery inventories, and Kubernetes informer caches remain in memory; they do not establish whether a claim or attachment is prepared.

The implementation must preserve these invariants:

1. A successful prepare or NRI attachment response has a committed record containing enough information to retry and clean up its effects.
2. A failed database operation never becomes an apparent successful preparation in a separate memory cache. Missing records and database failures are distinct outcomes.
3. Each claim UID belongs to one pod UID, and each non-shareable PCI device has one owner. Deleting one claim preserves its sibling claims and their shared pod artifacts.
4. Host mutation always follows durable intent. An interrupted operation retains its recovery information until cleanup succeeds.
5. Shutdown and an NRI disconnect do not unprepare devices belonging to running workloads or erase their records.

bbolt provides a single writer, concurrent read transactions, and a consistent view within each transaction. Its transaction-owned byte slices must not escape without copying, and transactions must remain short to avoid remapping/deadlock problems. These properties support the database design; they do not make CNI, sysfs, files, and Kubernetes writes one atomic operation. See the [bbolt transaction documentation](https://github.com/etcd-io/bbolt/blob/main/README.md#transactions) and [value lifetime documentation](https://github.com/etcd-io/bbolt/blob/main/README.md#using-keyvalue-pairs).

## Existing implementation and failure windows

| Location | Current behavior | Required change |
| --- | --- | --- |
| `pkg/podmanager/podmanager.go` | Loads `checkpoint.json` into `preparedClaimsByPodUID`; `Set`, delete methods, and network-data updates mutate memory before rewriting the whole checkpoint. Getters expose the stored pointers. | Replace both representations with transactional reads and writes; return independent snapshots and explicit errors. |
| `pkg/podmanager/podmanager.go:DeleteClaim` | Finds one matching claim, then deletes its entire pod entry. | Delete only that claim's key. Derive pod membership from remaining records and reject unsupported sharing. |
| `pkg/types/types.go` | Defines the map aliases, `PreparedDevice`, and checksum-based `Checkpoint`/`CheckpointV1`. | Reuse `PreparedDevices` inside a versioned claim record. Keep the old checksum decoder only for migration. |
| `pkg/driver/dra_hook.go` | Checks the map, mutates host devices and files, then calls `Set`. A persistence error triggers cleanup after memory has already changed. Global pod CDI is written after individual claims were saved. | Persist intent before host changes; commit prepared state only after required artifacts exist. Separate pre-existing claims from claims created by the current call during rollback. |
| `pkg/devicestate/state.go` and `pkg/host/host.go` | `BindDeviceDriver` reads and changes the driver before returning `OriginalDriver`; device-info and claim CDI files are written afterward. `Unprepare` deletes both claim and pod CDI files. | Split planning/inspection from application so original state is recorded before mutation. Move shared pod-artifact ownership out of claim-local cleanup. |
| `pkg/nri/nri.go` | Mutates returned `PreparedDevice.NetAttachDefConfig`; stores network data asynchronously, after fetching the ResourceClaim from Kubernetes. Its channel carries pointers and is not durable. | Persist attachment intent and results synchronously; store pending Kubernetes status updates inside each claim record. |
| `pkg/cni/cni.go` | Executes ADD/DEL using sandbox ID, netns, interface name, and NAD configuration. It has a DEL retry without netns, but no database attachment identity or progress record. | Store those exact inputs before ADD and reuse them during DEL/recovery. |
| `cmd/dra-driver-sriov/main.go` | Creates one PodManager and shares it between driver and NRI; no persistent-store close lifecycle exists. Several initialization failures return after driver startup. | Own the database and all cleanup at the composition root, including partial initialization. |
| `pkg/driver/health.go` and `deployments/helm/dra-driver-sriov/templates/dra-driver.yaml` | Health accepts only empty/`liveness` services and probes an empty prepare RPC; the chart has no readiness probe. | Add a separate `readiness` service using store/recovery readiness and a matching chart probe. Keep process liveness separate from storage recovery. |

Existing tests in `pkg/podmanager/podmanager_test.go` reopen the same checkpoint through multiple simultaneously alive managers and explicitly accept whole-pod deletion and duplicate claim ownership. Replace those expectations. bbolt's exclusive writer lock means reopen tests must close the first manager before creating another.

## Store ownership and layout

Add `pkg/preparedstate` containing a `Store` interface, a bbolt implementation, a versioned claim wrapper, validation, and a legacy importer. The driver and NRI depend on the interface. Remove the authoritative `pkg/podmanager` implementation rather than adding bbolt underneath an unchanged map. Reuse `types.PreparedDevices` as the JSON device payload, as the checkpoint already does. Its nested Kubernetes/CDI types become part of the versioned encoding: retain round-trip fixtures and provide a decoder migration when dependency or type changes alter that encoding.

Open `prepared-state.db` beneath `Config.DriverPluginPath()`, using the existing kubelet-plugin hostPath mount. Use one database handle per process, a finite open-lock timeout such as five seconds, file mode `0600`, and the existing protected parent directory. Keep `NoSync=false`, `NoGrowSync=false`, and normal durability defaults. Do not expose an unsafe asynchronous-write setting. Successful commits rely on the host filesystem and device honoring sync operations; a database cannot recover a lost node disk. The [bbolt API](https://pkg.go.dev/go.etcd.io/bbolt#DB) documents sync options, and its [database implementation](https://github.com/etcd-io/bbolt/blob/main/db.go) implements the exclusive writer lock.

Use exactly one bucket named `claims`. Its keys are claim UIDs, and its values are complete JSON claim records. The notation `claims/<claimUID>` means bucket plus key, not a nested bucket or a prefix repeated in every key. JSON remains an encoding, not a second checkpoint.

```text
prepared-state.db
  claims
    <claimUID>    -> JSON versioned claim record
```

Every entry is a claim record; there is no reserved metadata key. Keep the encoding version and node identity in each record. Reject empty claim keys and verify each key matches its recorded claim UID. An empty `claims` bucket is valid after initialization or removal of the final claim. Migration is selected by database/checkpoint file existence, without stored migration flags or source digests.

| JSON claim field | Contents and ownership |
| --- | --- |
| `version`, node identity, claim identity, `podUID`, pod namespace/name | Encoding version and the identities needed for lookups, validation, and recovery, including records with no completed device yet. |
| `revision`, preparation phase, configuration provenance/fingerprint | Legal lifecycle transitions, stale-operation checks, and approval of the allocation-time configuration. |
| `attachmentOwner`, annotation presence/digest | The Pod's pinned `nri` or `multus` selection, retained with each claim. Imported records may use `unknown` until recovery establishes the prior owner; they cannot authorize fresh attachment. All claims for the same Pod must agree. |
| `devices` | Existing `types.PreparedDevices` JSON, including approved effective configuration, frozen NAD bytes, PCI identity, original driver, interface assignments, CDI edits, and network metadata. |
| Operation progress | Pending/completed host and artifact steps, including updates to the shared Pod CDI file. |
| `attachments` | Embedded records containing sandbox ID, device identity, phase, claim incarnation, boot ID, frozen CNI input, interface/netns identity, result, and metadata progress. |
| `pendingUpdates` | Embedded desired status publications or metadata refreshes with target UID, operation ID, desired generation, and retry bookkeeping. This is the durable publication queue. |

The bare `PreparedDevices` slice lacks lifecycle fields, so the wrapper is required for the recovery guarantees in this plan. Device identity includes driver, pool, and device name. Add explicit original-binding knowledge to the device progress: an empty `OriginalDriver` must not silently mean both unknown and originally unbound. Preserve the existing restoration contract for imported legacy entries until inspection establishes a stronger one. Sharing remains unsupported throughout this lifecycle.

The first encoding version is `1`. Use explicit JSON field names and typed lifecycle constants; reject unsupported versions before side effects. A claim's `incarnation` is a newly generated operation UUID for each new preparation, even if the same ResourceClaim UID was previously unprepared. Start `revision` at one and increment it for each committed change. External completion tokens carry claim UID, incarnation, revision, and operation identity; a matching UID alone cannot authorize a stale callback after deletion and re-creation of a record. Store the configured node name with each record and reject records copied from another node. Read the Linux boot ID from `/proc/sys/kernel/random/boot_id` for attachment identities; a boot-ID change triggers reconciliation, not silent record deletion.

Use one shared, versioned fingerprint helper with the configuration resolver. Its canonical input includes the claim UID, sorted local `(request, driver, pool, device, shareID)` allocation keys, selected class names, and complete resolved configurations. Exclude mutable device status, resource versions, raw JSON whitespace, and generated interface names; retain applied names and resolved NAD contents separately. The store persists and compares the resolver's fingerprint rather than independently defining another hash. The policy/provenance fields record which resolver contract produced that configuration. Before the separate class-only feature lands, the existing resolver remains the policy; this storage change must not claim legacy configuration was administrator-approved. On deployment of class-only enforcement, its resolver decides whether reuse is permitted. Preserve legacy records for cleanup when that decision rejects new use.

For explicit legacy modes, persist `attachmentOwner=nri` for `STANDALONE` and `attachmentOwner=multus` for `MULTUS`; annotation fields may be absent. The unified-mode feature populates the Pod annotation snapshot when it introduces annotation-based routing. Do not make that feature a prerequisite for migrating the store.

### Lifecycle contract

| State | Allowed next action and durable result |
| --- | --- |
| No claim record | `BeginPrepare` reserves ownership and writes `preparing` before any device mutation. |
| `preparing` | Reconcile recorded work and commit `prepared`, or persist `unpreparing` and compensate. Do not return the planned devices as a prepared result. |
| `prepared` | Return a verified snapshot, change embedded attachment/publication progress, or transition to `unpreparing`. |
| `unpreparing` | Retry detach, binding restoration, and artifact cleanup. Delete the record only after cleanup and retirement of publication work. Never transition back to `prepared`; a later preparation gets a new incarnation. |

An attachment uses `add-pending -> attached -> delete-pending -> detached`. Failure or recovery can move `add-pending` directly to `delete-pending`; it cannot invent an `attached` result. Store each device's host step with its intended binding and observed original-binding state, and each file step with its target path and intended contents or deterministic reconstruction inputs. Keep bounded error information and retry timestamps for diagnosis, without changing ownership merely because retry limits are reached.

Claim lookups use `Bucket.Get`. Pod lookups scan and decode records within one read transaction, selecting matching `podUID` values. PCI conflict checks scan the same records within the write transaction that inserts preparation intent. Pending records retain ownership during cleanup. Recovery and publication workers scan embedded progress and `pendingUpdates`. These scans cost O(number of claims), but avoid maintaining secondary indexes or an authoritative memory cache. Measure the node-scale workload before proposing indexes as a later optimization.

For a new claim, read sibling claims within the same write transaction and reuse their pinned owner/annotation snapshot; reject disagreements. Resolve interface names across the sibling device payloads, including pending operations, and persist each assignment in its owning claim. Global Pod CDI is derived from that sibling set. The claim initiating a shared-file change retains its pending artifact step until the file matches the intended union; serialize such operations per Pod and reconcile pending steps before another operation or container-readiness check succeeds. Keep the final claim record until final shared-file cleanup completes. An individual VFIO device may require no CNI operation without changing the Pod owner.

Validate sibling records against the active configuration policy before publishing or rebuilding artifacts for new use, including during recovery. Rejected or unverified legacy records remain cleanup-only. Do not publish them through a valid sibling's global Pod CDI union or delete artifacts still needed by a live legacy workload. If new publication cannot safely preserve that distinction, block the Pod's new preparation with a migration error until its legacy workloads are drained.

## API, concurrency, and lifecycle

Expose domain operations rather than a public `*bolt.DB` or mutable `PreparedDevice` pointers:

- `GetClaim(claimUID)` uses the claim key; `GetPodDevices(podUID)` scans claim records. Both return decoded values plus `found` and `error`.
- `BeginPrepare(plan)` scans for PCI conflicts and sibling claims, validates trusted configuration, pod routing, and expected revisions, then creates durable preparation intent in the same write transaction.
- `RecordPrepareProgress`, `CommitPrepared`, `BeginUnprepare`, and `CompleteUnprepare` enforce legal transitions and expected revisions.
- `BeginAttachment`, `CommitAttachment`, `BeginDetach`, and `CompleteDetach` update embedded attachment records using their complete identity and frozen inputs.
- `ListRecoverable`, `ListPendingUpdates`, and `AcknowledgeUpdate` scan claim records and conditionally acknowledge the generation actually published within its owning record.
- `Close` is owned by `RunPlugin`; neither the driver nor NRI closes a shared store independently.

All reads decode inside `View`. Every mutation reads the latest JSON, validates its revision, changes the intended fields, and marshals/writes it within one `Update`. Never overwrite a whole record from a snapshot fetched before the transaction; concurrent NRI and prepare updates must preserve each other's fields. A transaction can update several claim keys atomically when shared Pod work requires it. Never hold a transaction across Kubernetes requests, CNI invocation, sysfs operations, CDI/device-info writes, or another store method. Use transaction-local helpers to avoid nested transactions. Check the final commit error. Avoid `Batch` initially; its callback may be retried, which complicates operation reasoning without helping this node-sized workload.

Use process-local pod operation locks to serialize preparation, teardown, NRI callbacks, recovery, and status publication that touch a pod's state or shared artifacts. These locks contain no prepared-device data. Scanning for conflicting PCI ownership and inserting the new claim intent in one write transaction prevents two different pod locks from authorizing the same device. Acquire multiple pod locks in a defined UID order, or split work per pod. Revision checks reject stale async results and results from a previous sandbox or preparation incarnation. They supplement locks and persist across process restarts. Bound external operations by their contexts so a stuck Kubernetes publication cannot hold the pod lock indefinitely; it leaves its pending generation for retry.

Open and validate the store before registering serving components. Create deferred cleanup immediately after each successful initialization. On shutdown, mark readiness unavailable, reject new work, stop kubelet and NRI callback admission, wait for admitted operations, cancel and join recovery/publication workers, then close bbolt. Preserve pending records if a bounded shutdown deadline interrupts external cleanup. Never close a channel while callbacks may send to it. Replace the current network-data channel with optional wake-up notifications for scanning `pendingUpdates`; losing a wake-up must not lose work. Registering NRI callbacks during synchronization is allowed while normal workload handling remains gated on recovery readiness.

## Transactions around external effects

### Preparation

1. Validate the ResourceClaim, allocation, configuration sources, pod identity, and flow before accepting an existing prepared record. Acquire the pod operation lock and read the current state. An unchanged, verified, fully prepared record can return its stored result; a conflicting fingerprint fails rather than silently overwriting live devices.
2. Resolve every device, trusted effective configuration, required NAD snapshot, interface name, and original host binding without changing the host. `BindDeviceDriver` must be refactored or supplemented with a plan/apply API: returning the original driver after binding is too late for crash-safe intent logging. Recheck expected binding immediately before applying a change.
3. Scan for ownership conflicts and commit one `preparing` claim record containing the selected pod flow, device payload, and intended artifacts. Abort without host mutation if this transaction fails.
4. Apply each host change and artifact outside the transaction, with its intent already persisted. Commit per-device progress/results when needed for reconstruction. Atomic file replacement and idempotent cleanup make CDI/device-info replay safe. Kernel module loading is shared node state and is not undone merely because one claim fails.
5. Generate claim CDI and the desired pod CDI union from sibling records plus this operation's planned devices. Commit `prepared` and completed artifact progress only after required files exist. Return success only afterward. A restart with `preparing` or pending artifact work reconciles before serving a prepare result.
6. Put desired Kubernetes status updates into this claim's `pendingUpdates` in the same final transaction. Kubernetes availability does not determine whether the locally committed device exists.

Multi-claim RPCs are coordinated per pod; they are not a cross-host transaction. Validate each claim's single supported Pod reservation and group claims by Pod UID before assigning interfaces or generating global CDI. Never use the first claim's Pod as the owner of the whole RPC. Invalid claims get per-claim errors without applying their effects. Record which preparations were newly started by the call. An error must not roll back a valid preparation that predated it. If a global pod artifact update fails, return an error for affected new preparations and retain durable cleanup intent. Preserve sibling claims while rebuilding their prior artifact union. Pod scans use sorted claim UIDs and retain each claim's persisted device order so replay does not renumber interfaces or change returned CDI assignments.

### Attachment and metadata

For an NRI-owned pod, persist an embedded `add-pending` attachment with sandbox ID and the complete resolved CNI input before each ADD. Run CNI outside the transaction, then synchronously persist `attached`, CNI result, network metadata, and `pendingUpdates` in the owning claim before acknowledging the hook. Apply required in-container metadata synchronously from that committed snapshot before workload startup; retain a pending refresh if it fails. Kubernetes status publication remains asynchronous.

Read failures are hook errors. A missing store record is not automatically proof that a pod does not use this driver: verify the pod's claims/routing before treating it as an unrelated workload. Multus-owned pods never trigger this store's NRI ADD/DEL path. The selected owner governs cleanup even if a pod annotation changes later.

If ADD succeeds but its result commit fails, return an error and attempt compensation using the persisted intent and the local result. If compensation or its acknowledgment fails, retain `add-pending` and block device reuse. There is an unavoidable crash window between an external effect and its database acknowledgment. Recovery provides retryable, inspected operations; it cannot promise exactly-once CNI execution. Do not blindly issue ADD again for an ambiguous live sandbox. Inspect the runtime/CNI cache and namespace identity; if the supported plugins cannot establish success, require safe teardown and recreation instead.

For a partial multi-device failure, record cleanup work for every previously attached or possibly attached device. DEL retries use the exact stored config, sandbox ID, and interface, including the existing missing-netns fallback in `pkg/cni/cni.go`. Persist `delete-pending` before DEL and `detached` only after success. A sandbox restart receives a new attachment identity; stale results from the previous sandbox cannot overwrite it.

Provide a store operation for the runtime-enforcement feature's `CreateContainer` reconnect check: for an NRI-owned pod, require matching current sandbox and boot identities plus committed `attached` records for every required network device and completion of required metadata refresh. The runtime's plugin-presence validator alone cannot prove that this sandbox's `RunPodSandbox` hook executed. A sandbox created while the driver was disconnected must not start application or init containers merely because the plugin has since reconnected. Runtime synchronization can establish observed sandbox identity, but must never synthesize attachment success. Repair only when inspection proves it safe; otherwise the enforcement feature rejects container creation until sandbox recreation completes the normal attachment sequence. Devices intentionally requiring no CNI are checked against their committed preparation, not an invented attachment. This feature owns persistence and the readiness predicate; the runtime-enforcement feature owns callback registration and runtime/admission deployment.

### Unprepare

Commit `unpreparing` before detaching driver-owned attachments or restoring devices. Never restore a VF still used by an active sandbox. Use runtime synchronization and kubelet lifecycle ordering to establish teardown; an inconclusive runtime/API read retains ownership and returns a retryable error.

Clean only that claim's device-info and CDI artifacts. Scan sibling claims to rebuild the global pod CDI union; remove the file only after the final claim's cleanup succeeds. Persist progress so repeated DEL, driver restoration, or file removal can finish after a crash. Serialize status publication with cleanup so an old preparation update cannot finish after teardown; complete or explicitly retire pending publication work before removing its record. Finally delete only the claim UID key in one transaction. Its embedded attachments and pending work disappear with it, and sibling records remain intact. A failed final delete leaves an `unpreparing` record, never an apparently prepared claim whose hardware has already been restored. Repeated unprepare of a confirmed absent claim succeeds.

## Recovery and fail-closed behavior

Startup validates the `claims` bucket, each record's node identity and version/decoding, claim key/identity agreement, duplicate PCI ownership, sibling routing consistency, and bbolt structural integrity before normal serving. Reconcile embedded pending preparation/cleanup, shared artifact work, and publication updates. Do not silently drop corrupt entries or replace a failed database with an empty one.

Committed prepared records are the authority for desired ownership, but they are not proof that hardware, files, or a runtime sandbox still exist. Recreate missing derived files when safe; inspect host identity before rebinding anything. Use NRI runtime synchronization to associate existing sandboxes with their stored IDs, and distinguish a process restart from a node reboot using boot identity. Do not use Kubernetes pod existence alone as proof that a specific sandbox is running. If sandbox state cannot be established, retain ambiguous attachments and defer destructive recovery. A missing or replaced device is unavailable until reconciliation resolves it.

The worker scans `pendingUpdates` inside claim JSON records and uses at-least-once publication with idempotent, UID-checked status merges. Continue the existing conflict-safe merge in `pkg/types/statusretry.go`; retain other drivers' status. Acknowledgment rereads the owning claim inside `Update` and removes only the matching operation/generation so a newer desired update is not lost. It never recreates an absent record. ResourceClaim replacement under the same name never inherits old state.

Disk-full, permission, lock, corruption, schema, and commit failures surface as actionable errors and make readiness fail when state safety is uncertain. Stop admitting new prepares/attachments when durability cannot be guaranteed. Leave running workloads and their files alone. Recovery must never treat a database failure as an empty successful device set.

If the database is absent and the checkpoint exists, run the first-load importer below. If neither exists, initialize an empty database only when no driver artifacts or observed runtime/kubelet evidence indicates existing ownership. Otherwise report lost state; do not silently bootstrap an empty database over live allocations.

## First-load checkpoint migration and rollback

Add `migrateCheckpointIfNeeded(databasePath, checkpointPath)`. It imports the checkpoint only when the final database file does not exist and the checkpoint does. After a successful durable import, remove the checkpoint. No migration completion flag, checkpoint digest, or checkpoint archive is maintained.

| Files at startup | Behavior |
| --- | --- |
| Database exists | Open and validate it; use it as authoritative state. Never import a checkpoint over it. If a checkpoint remains from interrupted cleanup, remove it only after database validation succeeds. |
| Database absent, checkpoint exists | Validate and import the checkpoint, durably install the database, then remove the checkpoint. |
| Both absent | Initialize a database with an empty `claims` bucket, subject to the lost-state check above. |

Run the existence check and initialization under a stable advisory startup lock, with the old checkpoint writer stopped. Call the migration function before normal `bbolt.Open` could create the final database file. Hold the startup lock until the final database handle owns its bbolt lock. Filesystem errors other than not-found are startup errors, not evidence that a file is absent.

The import function performs these steps:

1. Read the checkpoint with its existing checksum verifier. Validate the complete payload, including Pod/claim UIDs, duplicate PCI ownership, nil entries, device identity, and restoration/configuration fields. Empty checkpoints are valid. A validation failure preserves the checkpoint and fails startup.
2. Flatten the Pod/claim map into one versioned JSON record per claim UID, preserving the existing `PreparedDevices` payload. Apply the legacy-state checks below when populating lifecycle fields. Importing state must not execute host or CNI operations.
3. Create a temporary database in the same directory and write the `claims` bucket and all records in one transaction. Sync, close, and validate it; atomically rename it to the final database path and sync the parent directory. Initialize fresh empty databases through the same temporary-file installation path. The final path must never expose a partially imported or uninitialized database that would cause the next startup to skip import.
4. Open and validate the installed database, then remove the checkpoint and sync the parent directory. If checkpoint removal fails, retain the database and report the error. On retry, the existing-database branch completes checkpoint cleanup without importing it again.

A crash before installation leaves the checkpoint available for another import; discard an abandoned temporary database under the startup lock and rebuild from the checkpoint. A crash after installation but before checkpoint removal leaves both files, handled by the existing-database branch. An unreadable or corrupt existing database must fail startup without deleting or importing the checkpoint. These file operations make the first load retryable without migration metadata.

Legacy state still needs the same runtime validation as other prepared state. Determine its flow from the explicit legacy `STANDALONE`/`MULTUS` setting and consistent stored fields; a new unified setting or an empty NAD alone cannot identify the prior owner. Preserve unresolved ownership for recovery instead of guessing from current Pod annotations. Legacy checkpoints also lack trusted configuration provenance and reliable attachment phase. Only authorize reuse when the UID-matched allocation and applied configuration satisfy the active resolver policy and runtime reconciliation establishes attachment state. If class-only enforcement is enabled, that includes its provenance checks. Otherwise preserve the record for existing attachment recovery and cleanup, without authorizing new prepare replay or fresh ADD. Per-claim configuration-policy versions and fingerprints remain part of normal claim validation.

A new database plus a stale checkpoint is not a supported downgrade state. An old binary would ignore bbolt and could create or read an empty/stale checkpoint. The rollback procedure therefore drains and cleans the node while the new binary still understands pending operations, confirms no claims/attachments remain, stops it, archives the database, and only then installs the old release. Live downgrade requires a separately designed export tool with strict representability checks and is outside this implementation. Back up a live database through a bbolt transaction snapshot, not a raw copy of changing database bytes; restoration requires reconciling the restored snapshot against actual node state. The [bbolt backup documentation](https://github.com/etcd-io/bbolt/blob/main/README.md#database-backups) describes consistent snapshot copying.

## Implementation sequence

1. Add a reviewed, pinned `go.etcd.io/bbolt` dependency compatible with the repository's Go toolchain. Add the single `claims` bucket, versioned JSON wrapper around `PreparedDevices`, scan helpers, codec validation, domain errors, transaction tests, and a test fault boundary around commit results.
2. Add `migrateCheckpointIfNeeded`, open validation, and `Close`; integrate ownership into `RunPlugin`, including cleanup on initialization failure. Check file existence before opening the final database, import only when it is absent, and remove the checkpoint after durable installation. Keep checkpoint decoding isolated in this first-load function.
3. Replace all PodManager reads/writes in driver and NRI with store APIs and decoded snapshots. Remove the prepared map aliases where no longer needed. Change tests to close stores and enforce UID/PCI invariants.
4. Refactor host/device preparation into inspect, persist intent, apply, and finalize steps. Implement per-pod serialization and safe shared CDI lifecycle. Change rollback to preserve pre-existing and sibling claims.
5. Add embedded attachment progress and pending updates, synchronous local network-data persistence, publication retries, and startup/runtime reconciliation. Remove pointer-based asynchronous persistence.
6. Add crash/failure tests and restart coverage in `test/e2e/` using `test/e2e/framework`, `hack/virtual-cluster-redeploy.sh`, and `.github/workflows/virtual-e2e-singlenode.yaml` / `virtual-e2e-multinode.yaml`. Extend `.github/actions/run-e2e-workloads/action.yml` only as needed to run and report the new cases. Document migration, backup, lock failure, corruption recovery, disk-full behavior, and drained downgrade in operator documentation.

Each intermediate change must preserve recoverable state. Do not ship a transitional release that writes both a mutable prepared map/checkpoint and bbolt as competing authorities.

## Test and acceptance matrix

| Area | Required evidence |
| --- | --- |
| Store contract | Assert exactly one `claims` bucket, only claim UID keys, and no nested buckets or reserved metadata entries. Test create/read/update/delete, real close/reopen persistence, existing `PreparedDevices` JSON round trips, copied snapshots, Pod scans, deterministic device order, UID/PCI ownership rejection, and claim deletion preserving siblings. The bucket is empty after the last claim is removed. |
| Transactions | Inject failure before commit and at commit reporting; verify no partial JSON replacement or partial multi-claim commit, no returned prepared success, and retained intent for ambiguous external effects. Exercise closed DB, lock timeout, wrong schema, malformed records, and disk-full behavior through a controlled filesystem/subprocess harness. |
| Concurrency | Run affected Go suites with `-race`; concurrent prepare/NRI/update/unprepare for one pod, different pods scanning for the same PCI device, stale snapshots/generations, and database close while an operation is admitted. Prove that field updates preserve concurrent changes, ownership conflicts are rejected, and sibling routing stays consistent. |
| Migration | Cover all file-existence combinations, checksum validation, multiple claims flattened to claim keys, empty checkpoint import, untrusted config, VFIO without NAD, invalid ownership, and stat/permission errors. Failures before installation preserve the checkpoint and leave no final database; success preserves all imported records and removes the checkpoint. Test crashes before/after rename, leftover temporary files, checkpoint-removal failure/retry, and both files remaining after a crash. A corrupt existing database preserves the checkpoint; a valid existing database is never overwritten by it. |
| Host crash boundaries | Kill a subprocess after intent, after bind, after each file write, before prepared commit, after cleanup, and before claim deletion. Reopen the same database and demonstrate recovery without lost original binding or device reuse during uncertainty. |
| NRI/CNI crash boundaries | Cover ADD success before result commit, partial multi-device ADD, duplicate events, missing netns DEL, old/new sandbox IDs, NRI disconnect, and metadata failure. Prove the store's readiness predicate rejects a sandbox without current attachment evidence; with the runtime-enforcement feature integrated, verify the same through `CreateContainer`. A live ambiguous attachment is never blindly attached again. |
| Pending updates | Kubernetes unavailable or conflicting while local prepare/attachment succeeds; restart worker and scan embedded updates; verify UID-safe publication, conditional acknowledgment, preservation of newer generations and other drivers' status, and no publication after claim cleanup. |
| Virtual-cluster behavior | Restart or kill the DaemonSet pod with prepared workloads; reuse the hostPath database; verify interface/CDI/metadata continuity, later cleanup, sibling-claim preservation, and original driver restoration. Include both NRI and Multus ownership and a node-reboot scenario where supported. |

Run existing suites for `pkg/driver`, `pkg/devicestate`, `pkg/nri`, `pkg/cni`, and `pkg/types` alongside the new store suites. Replace checkpoint-specific tests with migration fixtures and store invariants. Acceptance requires exactly one `claims` bucket, versioned JSON reusing the existing device payload, no runtime checkpoint writes, and no authoritative prepared-device map in production code.

Use `go test -race ./pkg/preparedstate/... ./pkg/driver/... ./pkg/devicestate/... ./pkg/host/... ./pkg/cdi/... ./pkg/nri/... ./pkg/cni/... ./pkg/types/...` for affected package validation, then the repository's `make check`, `make test`, and `make build` gates. The new `preparedstate` package does not exist until implementation. Run `make e2e-workloads` against each supported mode's virtual cluster with the new restart/migration cases selected by Ginkgo labels. Tests must assert observable interface presence, PCI binding, claim status/metadata, preserved sibling CDI, and final database cleanup; a driver Pod becoming Ready is insufficient evidence of recovery.

Use test-only fault injection and a subprocess harness to kill the writer at named durable boundaries; do not add a production flag that can disable synchronization or force corruption. A storage-failure test must distinguish a definitely uncommitted write from an ambiguous commit result, reopen the database, and reconcile from whichever durable state is present. Report environment-dependent node reboot and storage-exhaustion scenarios explicitly in CI results; do not count skipped cases as passed acceptance.

## Operational visibility and rollout

Log store path, schema version, startup action (open, initialize, or import), and recovery outcome without logging raw NAD configuration. Identify failed operations with claim UID, Pod UID, incarnation, phase, and error class. Add a gRPC `readiness` service in `pkg/driver/health.go` and a corresponding readiness probe in `deployments/helm/dra-driver-sriov/templates/dra-driver.yaml`; expose a positive health port in deployment values when probes are enabled. The existing empty-RPC liveness check cannot detect pending recovery. The readiness predicate requires an open, validated, writable store and completion of recovery needed for safe admission. Share this predicate with the other features rather than implementing competing health servers. Retain liveness while recovery can make progress. Add bounded counters/gauges for commit failures, recoverable claims/attachments, pending publications, and recovery failures through the existing metrics endpoint; avoid claim UIDs as metric labels.

Roll out to one drained canary node first, then test import and restart on prepared workloads before expanding node by node. There is no dual-write period and no opt-in flag selecting competing state stores. Keep the existing hostPath mount stable across upgrades. The release notes must call out the database lock, first-load checkpoint deletion, persistent disk requirement, and drained downgrade procedure. Store corruption, a wrong-node database, or missing state with live ownership requires operator-assisted reconciliation; deleting the database is not an automated repair.

## Dependencies on the other plans

This storage feature can land before the other features while preserving the existing configuration policy and explicit deployment modes. Integration must satisfy the following contracts; no sibling design file is required to understand the store and migration implementation.

The configuration-security feature supplies trusted effective configuration and policy provenance before either a fresh prepare or cached-result return once that policy is enabled. This plan must not persist or replay tenant configuration that the active security policy rejects.

The unified-mode feature supplies Pod routing and annotation validation before `BeginPrepare` when unified mode is selected. It uses the persisted `attachmentOwner` for both attachment and cleanup and must not infer ownership from NAD presence or the current global mode after restart.

The runtime-enforcement feature prevents protected workloads from starting when the NRI plugin is unavailable. Database failure must affect readiness and NRI service availability so runtime enforcement can protect startup. Readiness probes alone do not block container creation; before that feature lands, this proposal makes no runtime-wide missing-plugin guarantee. A healthy database alone is not evidence that the required plugin is connected or that its callbacks succeeded.
