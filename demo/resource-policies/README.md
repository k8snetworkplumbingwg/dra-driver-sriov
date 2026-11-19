# Resource Policy Demo

This demo showcases how to use `SriovResourcePolicy` to control which SR-IOV Virtual Functions are advertised as Kubernetes resources based on various hardware and configuration criteria.

DeviceClass `demo-resource-policies` is administrator-owned; workloads contain no SR-IOV
opaque configuration. Applying the bundle requires permission to manage
its classes, policies, and NADs; the `dra-driver-sriov` namespace must exist.
Run the driver in STANDALONE mode with kernel-bound VFs.

## Overview

This scenario demonstrates:
- Creating resource policies based on vendor IDs, Physical Function names, and other hardware attributes
- Setting up multiple resource configurations for different network interfaces
- Deploying a pod that uses policy-filtered SR-IOV resources with specific network requirements

## Components

### 1. SriovResourcePolicy
The `SriovResourcePolicy` resource defines which SR-IOV devices should be advertised as allocatable resources:
- **nodeSelector**: Targets SR-IOV-capable nodes (`feature.node.kubernetes.io/network-sriov.capable=true`)
- **configs**: Defines multiple resource configurations:
  - `eth0_resource`: Filters devices connected to eth0 Physical Function
  - `eth1_resource`: Filters devices connected to eth1 Physical Function
- **resourceFilters**: Specifies filtering criteria:
  - Vendor ID filtering (Intel: `8086`)
  - Physical Function name filtering (`pfNames`)
  - Optional filters for device IDs, PCI addresses, NUMA nodes, and drivers

### 2. Networking Setup
- Creates a dedicated namespace (`vf-test4`)
- Sets up NetworkAttachmentDefinition with SR-IOV CNI configuration
- Configures IPAM with host-local plugin for subnet `10.0.1.0/24`

### 3. Resource Allocation
- ResourceClaimTemplate requests exactly 1 VF from `eth1_resource`
- The administrator DeviceClass uses CEL to require `eth1_resource`
- Selects `demo-resource-policies`; its administrator VfConfig supplies the interface name and protected NAD

### 4. Pod Deployment
- Deploys a toolbox pod with network capabilities
- Claims the filtered SR-IOV resource
- Runs with privileged networking capabilities (NET_ADMIN, NET_RAW)

## Usage

1. Save and remove any catch-all policy that would otherwise claim the same VFs first.
   Policy evaluation is first-match (policies sorted by name), so something like
   `all-devices` prevents `eth0_resource` / `eth1_resource` attributes from being stamped:

   ```bash
   POLICY_BACKUP_DIR=$(mktemp -d)
   if kubectl -n dra-driver-sriov get sriovresourcepolicy all-devices \
     --ignore-not-found -o json > "$POLICY_BACKUP_DIR/original.json"; then
     jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp,
             .metadata.generation, .metadata.managedFields, .status)' \
       "$POLICY_BACKUP_DIR/original.json" > "$POLICY_BACKUP_DIR/all-devices.json" &&
       kubectl -n dra-driver-sriov delete sriovresourcepolicy all-devices --ignore-not-found
   fi
   printf 'Policy backup directory: %s\n' "$POLICY_BACKUP_DIR"
   ```

   These commands use `jq` to save a restorable policy without server metadata.
   Stop if backup/removal fails. Keep the backup directory and use the same Bash
   session for cleanup, or reset `POLICY_BACKUP_DIR` to the printed path. If no
   original policy existed, the backup is empty and cleanup must not create one.

2. Apply the resource policy to advertise SR-IOV resources:
   ```bash
   kubectl apply -f resource-policy.yaml
   ```

3. The DRA driver will discover SR-IOV devices and advertise only those matching the policy criteria
4. Pods can then claim resources using the advertised resource names
5. The pod will be scheduled on nodes where matching resources are available

**Note**: Without a matching `SriovResourcePolicy`, no devices will be advertised (opt-in model).

## Key Features

- **Opt-In Model**: Devices are only advertised when explicitly defined in a policy
- **Granular Filtering**: Filter by vendor, device ID, PCI address, PF name, NUMA node, or driver
- **Multi-Resource Support**: Configure multiple resource types on the same node
- **CEL Integration**: Use Common Expression Language for advanced resource selection
- **Network Integration**: Seamless integration with SR-IOV CNI plugin

## Prerequisites

- Kubernetes cluster with DRA (Dynamic Resource Allocation) support
- SR-IOV capable hardware
- SR-IOV Network Operator installed
- Physical Functions (eth0, eth1) configured on target nodes

## Cleanup

Delete the workload before removing the administrator-owned class and NAD.
Deleting only the workload namespace leaves those resources behind.

```bash
kubectl delete pod pod0 -n vf-test4 --ignore-not-found --wait=true
kubectl delete -f resource-policy.yaml --ignore-not-found
```

After removing the temporary `example-resource-policy`, restore the saved
original policy. Skip restoration if none existed before setup:

```bash
if [ -s "$POLICY_BACKUP_DIR/all-devices.json" ]; then
  kubectl apply -f "$POLICY_BACKUP_DIR/all-devices.json"
fi
```
