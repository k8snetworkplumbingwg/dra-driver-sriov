# Multiple Virtual Functions Claim Demo

This demo shows how to request multiple SR-IOV Virtual Functions in a single resource claim for applications requiring multiple network interfaces.

DeviceClass `demo-multiple-vf` is administrator-owned; workloads contain no SR-IOV
opaque configuration. Applying the bundle requires permission to manage
its classes, policies, and NADs; the `dra-driver-sriov` namespace must exist.
Run the driver in STANDALONE mode with kernel-bound VFs.

## Overview

This scenario demonstrates:
- Requesting multiple Virtual Functions (2 VFs) in a single resource claim
- Automatic interface allocation and configuration
- Multi-interface networking setup for high-availability or load balancing scenarios

## Components

### 1. Networking Setup
- Creates dedicated namespace (`vf-test3`)
- NetworkAttachmentDefinition with SR-IOV CNI configuration
- IPAM configuration with host-local plugin for subnet `10.0.1.0/24`
- Network settings: VLAN 0, spoofing protection enabled, trust mode on

### 2. Multi-VF Resource Claim
The `ResourceClaimTemplate` requests multiple resources:
- **count: 2**: Requests exactly 2 Virtual Functions
- **deviceClassName**: Selects administrator class `demo-multiple-vf`
- The claim contains no driver configuration
- **DeviceClass VfConfig**: Supplies the administrator NAD for all allocated VFs

### 3. Pod Deployment
- Toolbox container for testing and debugging
- Claims multiple VFs through single resource claim template
- Network capabilities enabled (NET_ADMIN, NET_RAW)
- Privileged escalation for network configuration

## Use Cases

Multiple VF allocation is beneficial for:
- **High Availability**: Primary/backup interface configuration
- **Load Balancing**: Distribute traffic across multiple interfaces  
- **Network Segmentation**: Separate interfaces for different traffic types
- **Bandwidth Aggregation**: Combine multiple interfaces for higher throughput
- **Multi-tenant Applications**: Isolate traffic per tenant or service

## Network Interface Behavior

When multiple VFs are allocated:
1. **Automatic Naming**: With the default prefix, interfaces are named `vfnet0`, `vfnet1`, etc.
2. **Individual Configuration**: Each VF gets a distinct interface in the same Pod network namespace
3. **Shared NetworkAttachmentDefinition**: All VFs use the same CNI configuration
4. **Independent Operation**: Each interface operates independently

## Resource Allocation Process

1. **Request Processing**: DRA driver receives request for 2 VFs
2. **Device Discovery**: Driver finds available VFs from the same or different PFs
3. **Interface Allocation**: Two VFs are allocated and configured
4. **Network Setup**: SR-IOV CNI creates network interfaces in pod namespace
5. **Pod Scheduling**: Pod is scheduled on node with available resources

## Usage

1. Apply the configuration:
   ```bash
   kubectl apply -f multiple-vf-one-claim.yaml
   ```

2. Verify resource allocation:
   ```bash
   kubectl get resourceclaim -n vf-test3
   kubectl describe pod pod0 -n vf-test3
   ```

3. Check network interfaces in the pod:
   ```bash
   kubectl exec -n vf-test3 pod0 -- ip link show
   ```

## Expected Network Interfaces

Inside the pod, you should see:
- `lo`: Loopback interface
- `eth0`: Default pod network interface
- `vfnet0`: First SR-IOV interface
- `vfnet1`: Second SR-IOV interface (if more than 1 VF requested)

## Configuration Considerations

- **Resource Availability**: Ensure sufficient VFs are available on target nodes
- **NUMA Affinity**: Multiple VFs may be allocated from different NUMA nodes
- **Performance**: Consider NUMA locality for optimal performance
- **PF Limitations**: Check Physical Function limits for simultaneous VF allocation

## Prerequisites

- Multiple VFs configured on SR-IOV Physical Functions
- Sufficient resources available on target nodes
- SR-IOV Network Operator properly configured
- DRA-enabled Kubernetes cluster

## Cleanup

Delete the demo-owned workload, template, class and NAD explicitly. Do not delete
the full bundle, which also contains the potentially shared discovery policy.

```bash
(
  set -eu
  kubectl delete pod pod0 -n vf-test3 --ignore-not-found --wait=true
  kubectl delete resourceclaimtemplate multiple-vf -n vf-test3 --ignore-not-found
  kubectl delete deviceclass demo-multiple-vf --ignore-not-found
  kubectl delete network-attachment-definition demo-multiple-vf-net -n dra-driver-sriov --ignore-not-found
)
```

Before setup, record whether `dra-driver-sriov/all-devices` and namespace
`vf-test3` already exist. Remove the policy only if this demo created it and no
other workload depends on it. Remove the namespace only if this demo created it,
it is not shared, and its generated claims have finished cleanup. Run each
optional command only when its condition is satisfied:

```bash
kubectl delete sriovresourcepolicy all-devices -n dra-driver-sriov --ignore-not-found
kubectl delete namespace vf-test3 --ignore-not-found
```
