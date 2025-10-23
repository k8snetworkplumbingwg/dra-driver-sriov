# Multus Integration with Single Virtual Function Demo

This demo demonstrates the integration of DRA SR-IOV driver with Multus CNI for dynamic network attachment using a single Virtual Function.

## Overview

This scenario shows:
- Integration between DRA (Dynamic Resource Allocation) and Multus CNI
- Single SR-IOV Virtual Function allocation with Multus network attachment
- Automatic network interface provisioning through Multus annotations
- Deployment-based workload with SR-IOV networking

## Components

### 1. Namespace and Networking Setup
- Creates dedicated namespace (`vf-test7`)
- NetworkAttachmentDefinition with SR-IOV CNI plugin configuration
- Multus resource annotation: `k8s.v1.cni.cncf.io/resourceName: sriov/vf`
- IPAM setup using host-local plugin with subnet `10.0.2.0/24`
- Standard SR-IOV network settings (VLAN 0, spoofchk on, trust on)

### 2. Single VF Resource Claim
The `ResourceClaimTemplate` configuration:
- **count**: Implicitly 1 (single VF request)
- **deviceClassName**: `sriovnetwork.openshift.io`
- Automatically integrates with Multus through DRA driver

### 3. Deployment Configuration
- Uses Deployment for production-like workload management
- Single replica for testing purposes
- Multus network annotation: `k8s.v1.cni.cncf.io/networks: vf-test1`
- Container with necessary networking capabilities (NET_ADMIN, NET_RAW)
- Resource claim binding for the single VF

## Multus Integration Benefits

This integration provides:
- **Declarative Network Attachment**: Networks defined through annotations
- **Dynamic Resource Management**: DRA handles VF allocation lifecycle
- **Compatibility**: Works with existing Multus-aware applications
- **Flexibility**: Easy migration path from traditional SR-IOV device plugin
- **Multi-Network Support**: Foundation for complex multi-network scenarios

## How It Works

1. **Pod Creation**: Deployment creates pod with Multus network annotation
2. **DRA Allocation**: DRA driver allocates SR-IOV VF based on resource claim
3. **Multus Processing**: Multus CNI reads network annotation and DRA allocation
4. **Interface Creation**: SR-IOV CNI plugin creates network interface in pod
5. **IP Assignment**: IPAM assigns IP address from configured subnet
6. **Pod Ready**: Pod starts with both cluster and SR-IOV networking

## Network Interface Behavior

With Multus and single VF:
1. **Container Interfaces**:
   - `eth0`: Default pod network (cluster networking)
   - `net1`: SR-IOV interface attached via Multus (high-performance networking)

2. **Traffic Routing**:
   - Cluster traffic uses `eth0`
   - Application data plane can use `net1` for SR-IOV acceleration
   - Multus manages the lifecycle of additional network attachments

## Use Cases

Multus integration with single VF is ideal for:
- **CNF Workloads**: Cloud-native network functions requiring hardware acceleration
- **Migration from Device Plugin**: Moving from SR-IOV device plugin to DRA
- **Hybrid Deployments**: Mixed workloads with varied networking requirements
- **Telco Applications**: 5G/NFV workloads with control and data plane separation
- **Performance-Critical Services**: Applications needing SR-IOV with Kubernetes orchestration

## Usage

1. Deploy the configuration:
   ```bash
   kubectl apply -f multus-integration-single-vf.yaml
   ```

2. Verify deployment:
   ```bash
   kubectl get deployment -n vf-test7
   kubectl get pods -n vf-test7
   kubectl describe pod -n vf-test7 -l app=pod0
   ```

3. Check Multus network attachments:
   ```bash
   kubectl get network-attachment-definitions -n vf-test7
   kubectl describe network-attachment-definitions vf-test1 -n vf-test7
   ```

4. Verify resource claims:
   ```bash
   kubectl get resourceclaim -n vf-test7
   kubectl describe resourceclaim -n vf-test7
   ```

5. Check network configuration in the pod:
   ```bash
   # Get pod name
   POD_NAME=$(kubectl get pods -n vf-test7 -l app=pod0 -o jsonpath='{.items[0].metadata.name}')
   
   # List interfaces
   kubectl exec -n vf-test7 $POD_NAME -- ip link show
   
   # Check IP addresses
   kubectl exec -n vf-test7 $POD_NAME -- ip addr show
   
   # Verify SR-IOV interface
   kubectl exec -n vf-test7 $POD_NAME -- ip addr show net1
   ```

6. Test network connectivity:
   ```bash
   # Test cluster networking (eth0)
   kubectl exec -n vf-test7 $POD_NAME -- ping -c 3 kubernetes.default.svc.cluster.local
   
   # Test SR-IOV interface (net1) - requires target IP
   kubectl exec -n vf-test7 $POD_NAME -- ping -I net1 -c 3 <target-ip>
   ```

## Key Differences from Traditional SR-IOV

| Aspect | Traditional SR-IOV Device Plugin | DRA with Multus |
|--------|----------------------------------|-----------------|
| Resource Allocation | Device plugin framework | Dynamic Resource Allocation API |
| Lifecycle Management | Static pool | Dynamic claim-based allocation |
| Network Attachment | Multus resource annotation | DRA resource claim + Multus annotation |
| Flexibility | Limited to device plugin model | Full Kubernetes resource model |
| Extensibility | Device plugin constraints | Native Kubernetes resources |

## Deployment Characteristics

- **Replicas**: Configured for 1 replica (can be scaled)
- **Termination Grace Period**: 2 seconds for quick cleanup
- **Security Context**: Runs as root with privilege escalation for network setup
- **Resource Management**: Uses Kubernetes resource claims for VF allocation

## Prerequisites

- SR-IOV capable network interface
- SR-IOV Network Operator installed and configured
- Multus CNI installed on the cluster
- At least one Virtual Function available
- DRA-enabled Kubernetes cluster (v1.34+)
- Appropriate node labeling and SR-IOV configuration

## Troubleshooting

Common issues and solutions:

1. **Pod stuck in Pending**:
   - Check resource claim status: `kubectl describe resourceclaim -n vf-test7`
   - Verify VF availability on nodes
   - Check DRA driver logs

2. **Network interface not created**:
   - Verify NetworkAttachmentDefinition: `kubectl describe net-attach-def vf-test1 -n vf-test7`
   - Check Multus CNI logs on the node
   - Verify SR-IOV CNI plugin installation

3. **IP assignment fails**:
   - Check IPAM configuration in NetworkAttachmentDefinition
   - Verify subnet availability
   - Review SR-IOV CNI logs

## Related Demos

- **single-vf-claim**: Basic single VF allocation without Multus
- **multus-integration-multiple-vf**: Multus integration with multiple VFs
- **claim-for-deployment**: Deployment patterns with SR-IOV resources

