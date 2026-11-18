# Deployment with Virtual Function Claims Demo

This demo demonstrates deploying multiple replicas of an application with SR-IOV Virtual Function claims for container networking acceleration.

DeviceClass `demo-deployment-vf` is administrator-owned; workloads contain no SR-IOV
opaque configuration. Applying the bundle requires permission to manage
its classes, policies, and NADs; the `dra-driver-sriov` namespace must exist.
Run the driver in STANDALONE mode with kernel-bound VFs.

## Overview

This scenario shows:
- SR-IOV Virtual Function allocation using DRA (Dynamic Resource Allocation) for multiple pod replicas
- Standard kernel-mode networking with SR-IOV acceleration across deployment replicas
- Kubernetes Deployment with 2 replicas, each getting its own VF
- High-availability container deployment with high-performance networking

## Components

### 1. Networking Setup
- Creates dedicated namespace (`vf-test6`)
- NetworkAttachmentDefinition with SR-IOV CNI plugin configuration
- IPAM setup using host-local plugin with subnet `10.0.2.0/24`
- Standard SR-IOV network settings (VLAN 0, spoofchk on, trust on)

### 2. VF Resource Claim Template
The `ResourceClaimTemplate` configuration:
- **count**: Implicitly 1 per pod replica (each pod gets a single VF)
- **deviceClassName**: Selects administrator class `demo-deployment-vf`
- **Administrator DeviceClass parameters** (not claim configuration):
  - `ifName: net1`: Network interface name in each container
  - `netAttachDefName: demo-deployment-vf-net`: References the NetworkAttachmentDefinition
  - An omitted `driver` preserves the existing binding; this demo requires kernel-bound VFs

### 3. Deployment Configuration
- Kubernetes Deployment with 2 replicas
- Each replica is a toolbox container for testing and validation
- Standard networking capabilities (NET_ADMIN, NET_RAW)
- Resource claim template binding for VF allocation per pod
- Sleep command keeps containers running for testing

## Use Cases

Deployment with VF claims is ideal for:
- **High Availability**: Multiple instances with SR-IOV acceleration
- **Load Distribution**: Distributing network-intensive workloads
- **Production Workloads**: Applications requiring both availability and performance
- **Scaling**: Easily scale replicas up/down while maintaining SR-IOV capabilities
- **Testing at Scale**: Testing applications with multiple SR-IOV enabled instances

## Network Interface Behavior

With deployment and VF allocation:
1. **Per-Pod Interfaces**:
   - `eth0`: Default pod network (cluster networking)
   - `net1`: SR-IOV interface (high-performance networking, unique per pod)

2. **Traffic Routing**:
   - Cluster traffic uses `eth0` for all pods
   - Application traffic can use `net1` for performance on each pod
   - Both interfaces can be active simultaneously on each replica

## Allocation Process

1. **Deployment Creation**: Kubernetes creates 2 pod replicas
2. **Resource Request**: Each pod requests a VF through resource claim template
3. **Device Discovery**: DRA driver finds available VFs (requires 2+ available VFs)
4. **Driver Binding**: Each VF remains bound to kernel driver (default)
5. **Interface Creation**: SR-IOV CNI creates `net1` interface in each pod
6. **IP Assignment**: IPAM assigns unique IPs from configured subnet to each pod
7. **Pods Ready**: All pods start with both standard and SR-IOV networking

## Usage

1. Deploy the configuration:
   ```bash
   kubectl apply -f deployment-vf.yaml
   ```

2. Verify deployment:
   ```bash
   kubectl get deployments -n vf-test6
   kubectl get pods -n vf-test6
   kubectl describe deployment pod0 -n vf-test6
   ```

3. Check network configuration on all pods:
   ```bash
   # List all pods
   kubectl get pods -n vf-test6 -o wide
   
   # Check interfaces on each pod
   kubectl exec -n vf-test6 <pod-name> -- ip link show
   kubectl exec -n vf-test6 <pod-name> -- ip addr show
   kubectl exec -n vf-test6 <pod-name> -- ip addr show net1
   ```

4. Test network connectivity from multiple pods:
   ```bash
   # Test cluster networking (eth0) from both pods
   for pod in $(kubectl get pods -n vf-test6 -o name); do
     echo "Testing $pod:"
     kubectl exec -n vf-test6 $pod -- ping -c 2 kubernetes.default.svc.cluster.local
   done
   
   # Test SR-IOV interface (net1) - requires target IP
   for pod in $(kubectl get pods -n vf-test6 -o name); do
     echo "Testing SR-IOV on $pod:"
     kubectl exec -n vf-test6 $pod -- ping -I net1 -c 2 <target-ip>
   done
   ```

## Scaling Operations

Scale the deployment while maintaining VF claims:
```bash
# Scale up to 3 replicas
kubectl scale deployment pod0 -n vf-test6 --replicas=3

# Scale down to 1 replica
kubectl scale deployment pod0 -n vf-test6 --replicas=1

# Monitor scaling
kubectl get pods -n vf-test6 -w
```

## Performance Characteristics

- **Throughput**: Higher than standard pod networking across all replicas
- **Latency**: Lower latency compared to virtual interfaces for each pod
- **CPU Usage**: Efficient packet processing with SR-IOV acceleration per pod
- **Load Distribution**: Network load can be distributed across multiple VF-enabled pods
- **Availability**: Maintains service availability even if one replica fails

## Prerequisites

- SR-IOV capable network interface
- SR-IOV Network Operator installed and configured
- At least 2 Virtual Functions available (for 2 replicas)
- DRA-enabled Kubernetes cluster (v1.34+)
- Appropriate node labeling and device plugin setup
- Sufficient node resources to schedule multiple replicas

## Resource Requirements

- **VFs**: Minimum 2 Virtual Functions (1 per replica)
- **Nodes**: Can run on single node (if VFs available) or multiple nodes
- **Memory/CPU**: Standard deployment resource requirements
- **Network**: Adequate bandwidth for multiple high-performance interfaces

## Common Use Patterns

This deployment setup serves as foundation for:
- Production applications requiring both HA and performance
- Load-balanced services with SR-IOV acceleration
- Testing application behavior with multiple SR-IOV instances
- Implementing horizontal scaling with specialized network requirements
- Building resilient network-intensive microservices

## Cleanup

Delete the demo-owned workload, template, class and NAD explicitly. Do not delete
the full bundle, which also contains the potentially shared discovery policy.

```bash
(
  set -eu
  kubectl delete deployment pod0 -n vf-test6 --ignore-not-found --wait=true --cascade=foreground
  kubectl delete resourceclaimtemplate vf-test6 -n vf-test6 --ignore-not-found
  kubectl delete deviceclass demo-deployment-vf --ignore-not-found
  kubectl delete network-attachment-definition demo-deployment-vf-net -n dra-driver-sriov --ignore-not-found
)
```

Before setup, record whether `dra-driver-sriov/all-devices` and namespace
`vf-test6` already exist. Remove the policy only if this demo created it and no
other workload depends on it. Remove the namespace only if this demo created it,
it is not shared, and its generated claims have finished cleanup. Run each
optional command only when its condition is satisfied:

```bash
kubectl delete sriovresourcepolicy all-devices -n dra-driver-sriov --ignore-not-found
kubectl delete namespace vf-test6 --ignore-not-found
```
