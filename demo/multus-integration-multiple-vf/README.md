# Multus Integration with Multiple Virtual Functions Demo

This demo demonstrates the integration of DRA SR-IOV driver with Multus CNI for dynamic network attachment using multiple Virtual Functions.

## Overview

This scenario shows:
- Integration between DRA (Dynamic Resource Allocation) and Multus CNI
- Multiple SR-IOV Virtual Functions (2 VFs) allocation with Multus network attachments
- Automatic provisioning of multiple network interfaces through Multus annotations
- Deployment-based workload with multi-interface SR-IOV networking

## Components

### 1. Namespace and Networking Setup
- Creates dedicated namespace (`vf-test8`)
- NetworkAttachmentDefinition with SR-IOV CNI plugin configuration
- Multus resource annotation: `k8s.v1.cni.cncf.io/resourceName: sriov/vf`
- IPAM setup using host-local plugin with subnet `10.0.2.0/24`
- Standard SR-IOV network settings (VLAN 0, spoofchk on, trust on)

### 2. Multi-VF Resource Claim
The `ResourceClaimTemplate` configuration:
- **count: 2**: Requests exactly 2 Virtual Functions
- **deviceClassName**: `sriovnetwork.openshift.io`
- Automatically integrates with Multus through DRA driver
- Both VFs share the same network attachment definition

### 3. Deployment Configuration
- Uses Deployment for production-like workload management
- Single replica for testing purposes
- Multus network annotation: `k8s.v1.cni.cncf.io/networks: vf-test1,vf-test1`
  - Note: Network name is repeated twice for 2 VF attachments
- Container with necessary networking capabilities (NET_ADMIN, NET_RAW)
- Resource claim binding for multiple VFs

## Multus Integration with Multiple VFs

This advanced integration provides:
- **Multiple Network Attachments**: Each VF gets a separate network attachment
- **Dynamic Multi-Interface Management**: DRA handles lifecycle of multiple VFs
- **Flexible Network Configuration**: Different or same network definitions per VF
- **High Availability**: Multiple interfaces for redundancy and failover
- **Load Balancing**: Traffic distribution across multiple SR-IOV interfaces

## How It Works

1. **Pod Creation**: Deployment creates pod with multiple Multus network annotations
2. **DRA Allocation**: DRA driver allocates 2 SR-IOV VFs based on resource claim
3. **Multus Processing**: Multus CNI reads network annotations and DRA allocations
4. **Interface Creation**: SR-IOV CNI plugin creates multiple network interfaces in pod
5. **IP Assignment**: IPAM assigns IP addresses from configured subnet to each interface
6. **Pod Ready**: Pod starts with cluster network plus multiple SR-IOV interfaces

## Network Interface Behavior

With Multus and multiple VFs:
1. **Container Interfaces**:
   - `eth0`: Default pod network (cluster networking)
   - `net1`: First SR-IOV interface attached via Multus
   - `net2`: Second SR-IOV interface attached via Multus

2. **Traffic Routing Options**:
   - Cluster traffic uses `eth0`
   - Application can use `net1` and `net2` for:
     - Active-backup configuration
     - Load balancing
     - Traffic segregation by type
     - Bandwidth aggregation

3. **Interface Management**:
   - Each interface operates independently
   - Same or different network configurations possible
   - Individual IP addressing per interface

## Use Cases

Multus integration with multiple VFs is ideal for:
- **Cloud-Native Network Functions**: CNFs requiring multiple data plane interfaces
- **High Availability Networking**: Primary/backup interface configuration
- **Multi-Tenant Applications**: Separate VFs for different tenants or services
- **5G/Telco Workloads**: Multiple interfaces for N2, N3, N4, N6 interfaces
- **Load Balancing**: Traffic distribution across multiple hardware-accelerated interfaces
- **Network Segmentation**: Separate control and data plane networks
- **Bandwidth Aggregation**: Combining multiple VFs for higher throughput

## Multus Network Annotation Format

The annotation `k8s.v1.cni.cncf.io/networks: vf-test1,vf-test1` specifies:
- Comma-separated list of network attachment definitions
- Each entry creates a separate network interface
- Can reference the same or different NetworkAttachmentDefinitions
- Order determines interface naming (net1, net2, etc.)

Alternative annotation formats:
```yaml
# Same network, multiple times
k8s.v1.cni.cncf.io/networks: vf-test1,vf-test1

# Different networks
k8s.v1.cni.cncf.io/networks: vf-control,vf-data

# JSON format with interface names
k8s.v1.cni.cncf.io/networks: |
  [
    {"name": "vf-test1", "interface": "dataplane1"},
    {"name": "vf-test1", "interface": "dataplane2"}
  ]
```

## Usage

1. Deploy the configuration:
   ```bash
   kubectl apply -f multus-integration-multiple-vf.yaml
   ```

2. Verify deployment:
   ```bash
   kubectl get deployment -n vf-test8
   kubectl get pods -n vf-test8
   kubectl describe pod -n vf-test8 -l app=pod0
   ```

3. Check Multus network attachments:
   ```bash
   kubectl get network-attachment-definitions -n vf-test8
   kubectl describe network-attachment-definitions vf-test1 -n vf-test8
   ```

4. Verify resource claims:
   ```bash
   kubectl get resourceclaim -n vf-test8
   kubectl describe resourceclaim -n vf-test8
   ```

5. Check all network interfaces in the pod:
   ```bash
   # Get pod name
   POD_NAME=$(kubectl get pods -n vf-test8 -l app=pod0 -o jsonpath='{.items[0].metadata.name}')
   
   # List all interfaces
   kubectl exec -n vf-test8 $POD_NAME -- ip link show
   
   # Check all IP addresses
   kubectl exec -n vf-test8 $POD_NAME -- ip addr show
   
   # Verify first SR-IOV interface
   kubectl exec -n vf-test8 $POD_NAME -- ip addr show net1
   
   # Verify second SR-IOV interface
   kubectl exec -n vf-test8 $POD_NAME -- ip addr show net2
   ```

6. Test connectivity on each interface:
   ```bash
   # Test cluster networking (eth0)
   kubectl exec -n vf-test8 $POD_NAME -- ping -c 3 kubernetes.default.svc.cluster.local
   
   # Test first SR-IOV interface (net1)
   kubectl exec -n vf-test8 $POD_NAME -- ping -I net1 -c 3 <target-ip>
   
   # Test second SR-IOV interface (net2)
   kubectl exec -n vf-test8 $POD_NAME -- ping -I net2 -c 3 <target-ip>
   ```

7. Advanced networking tests:
   ```bash
   # Check routing table
   kubectl exec -n vf-test8 $POD_NAME -- ip route show
   
   # Test bandwidth on each interface
   kubectl exec -n vf-test8 $POD_NAME -- ethtool -S net1
   kubectl exec -n vf-test8 $POD_NAME -- ethtool -S net2
   
   # Check interface statistics
   kubectl exec -n vf-test8 $POD_NAME -- cat /proc/net/dev
   ```

## Resource Allocation Considerations

When allocating multiple VFs:
- **Availability**: Ensure sufficient VFs exist on target nodes
- **NUMA Affinity**: VFs may come from different NUMA nodes
- **Performance**: Consider NUMA locality for optimal performance
- **PF Capacity**: Check Physical Function limits for VF count
- **Network Topology**: Plan for network connectivity requirements

## Deployment Characteristics

- **Replicas**: Configured for 1 replica (scalable)
- **Termination Grace Period**: 2 seconds for quick cleanup
- **Security Context**: Runs as root with privilege escalation
- **Resource Management**: Uses Kubernetes resource claims for VF allocation
- **Network Isolation**: Each VF provides independent network path

## Performance Considerations

Multiple VF allocation impacts:
- **Throughput**: Aggregate bandwidth across multiple interfaces
- **Latency**: Consistent low latency per interface
- **CPU Usage**: Slightly higher due to multi-interface management
- **Memory**: Additional memory for per-interface buffers
- **Scalability**: Plan for resource consumption per pod

## Prerequisites

- SR-IOV capable network interface with multiple VFs configured
- SR-IOV Network Operator installed and configured
- Multus CNI installed on the cluster
- At least 2 Virtual Functions available per target node
- DRA-enabled Kubernetes cluster (v1.34+)
- Appropriate node labeling and SR-IOV configuration
- IPAM with sufficient IP addresses for multiple interfaces

## Troubleshooting

Common issues and solutions:

1. **Pod stuck in Pending**:
   - Check if enough VFs are available: `kubectl get nodes -o json | grep -i allocatable`
   - Verify resource claim status: `kubectl describe resourceclaim -n vf-test8`
   - Check DRA driver logs for allocation errors

2. **Only one network interface created**:
   - Verify Multus annotation has correct format and count
   - Check that resource claim allocated 2 VFs
   - Review Multus CNI logs on the node

3. **Network interfaces not configured**:
   - Verify NetworkAttachmentDefinition: `kubectl describe net-attach-def vf-test1 -n vf-test8`
   - Check SR-IOV CNI plugin installation and configuration
   - Review pod events: `kubectl describe pod -n vf-test8 -l app=pod0`

4. **IP assignment fails on one or more interfaces**:
   - Check IPAM subnet has enough available IPs
   - Verify IPAM configuration in NetworkAttachmentDefinition
   - Review SR-IOV CNI logs for IPAM errors

5. **Performance issues**:
   - Check NUMA affinity: VFs from different NUMA nodes may impact performance
   - Verify VF queue configuration
   - Monitor interface statistics for drops or errors

## Advanced Configuration Examples

### Different Networks for Each VF
```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: vf-control
  namespace: vf-test8
---
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: vf-data
  namespace: vf-test8
---
# In pod annotation:
k8s.v1.cni.cncf.io/networks: vf-control,vf-data
```

### Custom Interface Names
```yaml
annotations:
  k8s.v1.cni.cncf.io/networks: |
    [
      {"name": "vf-test1", "interface": "control"},
      {"name": "vf-test1", "interface": "data"}
    ]
```

## Related Demos

- **multiple-vf-claim**: Multiple VF allocation without Multus
- **multus-integration-single-vf**: Multus integration with single VF
- **claim-for-deployment**: Deployment patterns with SR-IOV resources
- **resource-alignment**: Advanced resource allocation and NUMA awareness

