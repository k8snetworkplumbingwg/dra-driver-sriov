# Multus Integration with Multiple Resource Claims Demo

This demo demonstrates advanced integration of DRA SR-IOV driver with Multus CNI using multiple independent resource claims, each attached to different network configurations.

## Overview

This scenario shows:
- Multiple independent DRA resource claims in a single pod
- Integration with Multus CNI using different NetworkAttachmentDefinitions
- Each resource claim attached to its own dedicated network
- Advanced multi-network configuration for complex networking scenarios
- Deployment-based workload with isolated network paths

## Components

### 1. Namespace and Networking Setup
- Creates dedicated namespace (`vf-test9`)
- **Two NetworkAttachmentDefinitions** with distinct configurations:
  - `vf-test1`: Resource name `sriov1/vf`, subnet `10.0.2.0/24`
  - `vf-test2`: Resource name `sriov2/vf`, subnet `10.0.2.0/24`
- Each network definition represents a separate SR-IOV resource pool
- Standard SR-IOV CNI settings (VLAN 0, spoofchk on, trust on)

### 2. Multiple Independent Resource Claims
The configuration uses:
- **Single ResourceClaimTemplate** (`vf-test9`): Defines the claim specification
- **Two resourceClaims** in the pod:
  - `sriov1`: First claim using the template
  - `sriov2`: Second claim using the template
- Each claim independently allocates one VF
- Both claims reference the same template but are separate resource allocations

### 3. Deployment Configuration
- Uses Deployment for production-like workload management
- Multus network annotation: `k8s.v1.cni.cncf.io/networks: vf-test1,vf-test2`
  - Each network maps to a different resource claim
- Container resources reference both claims:
  ```yaml
  resources:
    claims:
    - name: sriov1
    - name: sriov2
  ```
- Container with necessary networking capabilities (NET_ADMIN, NET_RAW)

## Key Differences from Other Demos

| Aspect | Multiple VFs (Single Claim) | Multiple Resource Claims |
|--------|----------------------------|-------------------------|
| Resource Claims | 1 claim with count=2 | 2 separate claims with count=1 each |
| VF Allocation | Both VFs from same allocation | Independent VF allocations |
| Network Attachment | Can use same network | Each claim can use different network |
| Resource Pools | Single resource pool | Separate resource pools possible |
| Flexibility | VFs treated as a group | VFs managed independently |
| Use Case | Homogeneous networking | Heterogeneous networking |

## How It Works

1. **Pod Creation**: Deployment creates pod with two resource claims
2. **First DRA Allocation**: DRA driver allocates VF for `sriov1` claim
3. **Second DRA Allocation**: DRA driver allocates VF for `sriov2` claim (independent)
4. **Multus Processing**: Multus CNI reads network annotations and matches to resource claims
5. **Interface Creation**: SR-IOV CNI creates two interfaces based on different network definitions
6. **IP Assignment**: IPAM assigns IP addresses to each interface
7. **Pod Ready**: Pod starts with cluster network plus two independently managed SR-IOV interfaces

## Network Interface Behavior

With multiple resource claims and Multus:
1. **Container Interfaces**:
   - `eth0`: Default pod network (cluster networking)
   - `net1`: First SR-IOV interface from `sriov1` claim (attached to `vf-test1` network)
   - `net2`: Second SR-IOV interface from `sriov2` claim (attached to `vf-test2` network)

2. **Resource Mapping**:
   - `sriov1` claim → `vf-test1` NetworkAttachmentDefinition → `net1` interface
   - `sriov2` claim → `vf-test2` NetworkAttachmentDefinition → `net2` interface

3. **Network Isolation**:
   - Each interface can have completely independent configuration
   - Different VLANs, QoS, IPAM configurations possible
   - Enables true network separation within a single pod

## Use Cases

Multiple independent resource claims are ideal for:
- **Network Function Virtualization**: CNFs requiring separate control and data planes
- **Multi-Tenant Isolation**: Different network paths for different tenants
- **Separated Traffic Types**: Distinct networks for management, control, and data traffic
- **5G/Telco Workloads**: Separate interfaces for different 3GPP interfaces (N2, N3, N4, N6)
- **Compliance Requirements**: Isolated networks for regulatory or security compliance
- **Hybrid Cloud**: Different networks for intra-cloud and inter-cloud communication
- **Service Chaining**: Multiple attachment points for complex service chains

## Advanced Networking Scenarios

### Different Resource Pools
Each NetworkAttachmentDefinition can reference different SR-IOV resource pools:
- `sriov1/vf`: Could be from Intel NICs
- `sriov2/vf`: Could be from Mellanox NICs
- Enables heterogeneous hardware in same pod

### Different Network Configurations
Each network can have completely different settings:
- Different VLANs per interface
- Different QoS policies
- Different IP subnets
- Different MTU sizes
- Different security policies

### Independent Lifecycle Management
- Each resource claim can be managed independently
- Supports dynamic attachment/detachment scenarios
- Enables zero-downtime network reconfiguration

## Usage

1. Deploy the configuration:
   ```bash
   kubectl apply -f multus-integration-multiple-resourceclaim.yaml
   ```

2. Verify deployment:
   ```bash
   kubectl get deployment -n vf-test9
   kubectl get pods -n vf-test9
   kubectl describe pod -n vf-test9 -l app=pod0
   ```

3. Check all NetworkAttachmentDefinitions:
   ```bash
   kubectl get network-attachment-definitions -n vf-test9
   kubectl describe network-attachment-definitions vf-test1 -n vf-test9
   kubectl describe network-attachment-definitions vf-test2 -n vf-test9
   ```

4. Verify both resource claims:
   ```bash
   kubectl get resourceclaim -n vf-test9
   kubectl describe resourceclaim -n vf-test9
   ```

5. Check resource claim template:
   ```bash
   kubectl get resourceclaimtemplate -n vf-test9
   kubectl describe resourceclaimtemplate vf-test9 -n vf-test9
   ```

6. Verify network interfaces in the pod:
   ```bash
   # Get pod name
   POD_NAME=$(kubectl get pods -n vf-test9 -l app=pod0 -o jsonpath='{.items[0].metadata.name}')
   
   # List all interfaces
   kubectl exec -n vf-test9 $POD_NAME -- ip link show
   
   # Check all IP addresses
   kubectl exec -n vf-test9 $POD_NAME -- ip addr show
   
   # Verify first SR-IOV interface (from sriov1 claim)
   kubectl exec -n vf-test9 $POD_NAME -- ip addr show net1
   
   # Verify second SR-IOV interface (from sriov2 claim)
   kubectl exec -n vf-test9 $POD_NAME -- ip addr show net2
   ```

7. Test connectivity on each interface:
   ```bash
   # Test cluster networking (eth0)
   kubectl exec -n vf-test9 $POD_NAME -- ping -c 3 kubernetes.default.svc.cluster.local
   
   # Test first SR-IOV interface (net1 - vf-test1 network)
   kubectl exec -n vf-test9 $POD_NAME -- ping -I net1 -c 3 <target-ip-1>
   
   # Test second SR-IOV interface (net2 - vf-test2 network)
   kubectl exec -n vf-test9 $POD_NAME -- ping -I net2 -c 3 <target-ip-2>
   ```

8. Verify resource claim to network mapping:
   ```bash
   # Check which VF is allocated to each claim
   kubectl get resourceclaim -n vf-test9 -o yaml
   
   # Verify network attachment status
   kubectl exec -n vf-test9 $POD_NAME -- ip -d link show net1
   kubectl exec -n vf-test9 $POD_NAME -- ip -d link show net2
   ```

## Resource Claim Configuration

The pod specification shows how multiple claims are configured:

```yaml
spec:
  containers:
  - name: ctr0
    resources:
      claims:
      - name: sriov1  # Reference to first claim
      - name: sriov2  # Reference to second claim
  resourceClaims:
  - name: sriov1
    resourceClaimTemplateName: vf-test9  # Both use same template
  - name: sriov2
    resourceClaimTemplateName: vf-test9  # But are independent allocations
```

## Architecture Benefits

1. **Flexibility**: Each claim can evolve independently
2. **Scalability**: Easy to add more claims without changing existing ones
3. **Isolation**: Complete separation between network paths
4. **Reusability**: Same template used for multiple claims
5. **Manageability**: Each network can be managed by different teams
6. **Compliance**: Audit and control each network independently

## Performance Considerations

- **Independent Allocation**: Each VF allocated separately, may come from different PFs
- **NUMA Awareness**: VFs might be on different NUMA nodes
- **Resource Constraints**: Requires sufficient VFs across resource pools
- **Overhead**: Slightly higher management overhead than single claim
- **Throughput**: Each interface provides full SR-IOV performance
- **Isolation**: Better isolation but may impact NUMA-local performance

## Prerequisites

- SR-IOV capable network interfaces with multiple VFs configured
- SR-IOV Network Operator with multiple resource pools configured
- Multus CNI installed on the cluster
- At least 2 Virtual Functions available (can be from same or different PFs)
- DRA-enabled Kubernetes cluster (v1.34+)
- Appropriate node labeling and SR-IOV configuration
- IPAM configuration with sufficient IP addresses

## Advanced Configuration Examples

### Different Subnets per Network
```yaml
# vf-test1: Management network
ipam:
  ranges:
    - subnet: "192.168.1.0/24"

# vf-test2: Data network  
ipam:
  ranges:
    - subnet: "10.0.0.0/16"
```

### Different VLAN Configuration
```yaml
# vf-test1: VLAN 100
config: |-
  {
    "type": "sriov",
    "vlan": 100,
    ...
  }

# vf-test2: VLAN 200
config: |-
  {
    "type": "sriov",
    "vlan": 200,
    ...
  }
```

### Mixed Hardware Resources
```yaml
# NetworkAttachmentDefinition for Intel NICs
annotations:
  k8s.v1.cni.cncf.io/resourceName: intel.com/sriov_netdevice

# NetworkAttachmentDefinition for Mellanox NICs
annotations:
  k8s.v1.cni.cncf.io/resourceName: mellanox.com/sriov_netdevice
```

## Troubleshooting

Common issues and solutions:

1. **Only one VF allocated**:
   - Verify both resource claims exist: `kubectl get resourceclaim -n vf-test9`
   - Check if sufficient VFs available in both pools
   - Review DRA driver logs for allocation failures

2. **Network interface mapping incorrect**:
   - Verify Multus network order matches resource claim order
   - Check NetworkAttachmentDefinition resource annotations
   - Review pod network status annotations

3. **One claim succeeds, other fails**:
   - Check resource pool availability for each claim
   - Verify node has VFs for both resource types
   - Review resource claim events

4. **IP conflicts between interfaces**:
   - Ensure different subnets for each network
   - Check IPAM configuration in each NetworkAttachmentDefinition
   - Verify no subnet overlap

5. **Pod stuck in ContainerCreating**:
   - Check both resource claims are satisfied: `kubectl describe resourceclaim -n vf-test9`
   - Verify Multus can find both network definitions
   - Review kubelet logs for CNI errors

## Cleanup

```bash
kubectl delete namespace vf-test9
```

This will clean up all resources including:
- Deployment and pods
- Both resource claims
- ResourceClaimTemplate
- Both NetworkAttachmentDefinitions
- Namespace

## Related Demos

- **multus-integration-single-vf**: Multus with single resource claim
- **multus-integration-multiple-vf**: Multiple VFs in single claim
- **multiple-vf-claim**: Multiple VFs without Multus
- **claim-for-deployment**: Deployment patterns with resource claims

## Best Practices

1. **Network Planning**: Plan subnet allocation carefully to avoid conflicts
2. **Resource Naming**: Use descriptive names for claims and networks
3. **Documentation**: Document which network serves which purpose
4. **Monitoring**: Monitor each interface independently
5. **Testing**: Test failover scenarios between interfaces
6. **Security**: Apply appropriate network policies per interface
7. **Resource Limits**: Understand node capacity for multiple allocations

