# Static Resource Claim Demo

This demo demonstrates using a static ResourceClaim for SR-IOV Virtual Function allocation, as opposed to the dynamic ResourceClaimTemplate approach.

DeviceClass `demo-resourceclaim` is administrator-owned; workloads contain no SR-IOV
opaque configuration. Applying the bundle requires permission to manage
its classes, policies, and NADs; the `dra-driver-sriov` namespace must exist.
Run the driver in STANDALONE mode with kernel-bound VFs.

## Overview

This scenario shows:
- Static SR-IOV Virtual Function allocation using DRA (Dynamic Resource Allocation)
- Pre-created ResourceClaim that exists independently of pods
- Direct resource claim binding without templates
- Simple container deployment with high-performance networking

## ResourceClaim vs ResourceClaimTemplate

### ResourceClaimTemplate (Dynamic)
- **Lifecycle**: Creates new ResourceClaim instances automatically when pods are created
- **Scope**: Template-based, generates claims per pod/deployment
- **Ownership**: Claims are owned by the consuming pods
- **Use Case**: Ideal for dynamic workloads, deployments, and scaling scenarios
- **Reference**: Pod references via `resourceClaimTemplateName`

### ResourceClaim (Static)
- **Lifecycle**: Pre-existing, manually created resource claims
- **Scope**: Static, exists independently of consuming pods
- **Ownership**: Claims exist independently, can be reused across pods
- **Use Case**: Ideal for shared resources, debugging, and explicit resource management
- **Reference**: Pod references directly via `resourceClaimName`

## Components

### 1. Networking Setup
- Creates dedicated namespace (`vf-test7`)
- NetworkAttachmentDefinition with SR-IOV CNI plugin configuration
- IPAM setup using host-local plugin with subnet `10.0.3.0/24`
- Standard SR-IOV network settings (VLAN 0, spoofchk on, trust on)

### 2. Static VF Resource Claim
The `ResourceClaim` configuration:
- **Pre-created**: Exists before any pods are scheduled
- **Independent**: Not tied to specific pod lifecycle
- **Reusable**: Can be referenced by different pods (sequentially)
- **deviceClassName**: Selects administrator class `demo-resourceclaim`
- **Explicit**: Direct control over resource allocation timing
- **Administrator DeviceClass parameters** (not claim configuration):
  - `ifName: net1`: Network interface name in the container
  - `netAttachDefName: demo-resourceclaim-net`: References the NetworkAttachmentDefinition
  - An omitted `driver` preserves the existing binding; this demo requires kernel-bound VFs

### 3. Pod Deployment
- Simple toolbox container for testing and validation
- Standard networking capabilities (NET_ADMIN, NET_RAW)
- **Direct resource claim reference** using `resourceClaimName`
- Sleep command keeps container running for testing

## Use Cases

Static ResourceClaim is ideal for:
- **Shared Resources**: Resources that need to be used by different pods over time
- **Debugging**: Easier to inspect and debug resource allocation issues
- **Explicit Control**: When you need precise control over resource lifecycle
- **Development/Testing**: Creating and managing resources independently for testing
- **Resource Pooling**: Pre-allocating resources for specific workloads
- **Administrative Control**: IT administrators managing resource allocation separately from workloads

## Network Interface Behavior

Same as dynamic ResourceClaimTemplate:
1. **Container Interfaces**:
   - `eth0`: Default pod network (cluster networking)
   - `net1`: SR-IOV interface (high-performance networking)

2. **Traffic Routing**:
   - Cluster traffic uses `eth0`
   - Application traffic can use `net1` for performance
   - Both interfaces can be active simultaneously

## Allocation Process

1. **Claim creation**: ResourceClaim exists before the Pod; allocation occurs during scheduling
2. **Resource Discovery**: DRA driver finds and reserves available VF
3. **Pod Creation**: Pod references existing ResourceClaim by name
4. **Claim Binding**: Pod binds to pre-existing resource claim
5. **Interface Creation**: SR-IOV CNI creates `net1` interface in pod
6. **IP Assignment**: IPAM assigns IP from configured subnet
7. **Pod Ready**: Pod starts with both standard and SR-IOV networking

## Usage

1. Deploy the configuration (ResourceClaim created first):
   ```bash
   kubectl apply -f resourceclaim-vf.yaml
   ```

2. Verify resource claim allocation:
   ```bash
   kubectl get resourceclaims -n vf-test7
   kubectl describe resourceclaim static-vf-claim -n vf-test7
   ```

3. Verify pod deployment:
   ```bash
   kubectl get pods -n vf-test7
   kubectl describe pod pod0 -n vf-test7
   ```

4. Check network configuration:
   ```bash
   # List interfaces
   kubectl exec -n vf-test7 pod0 -- ip link show
   
   # Check IP addresses
   kubectl exec -n vf-test7 pod0 -- ip addr show
   
   # Verify SR-IOV interface
   kubectl exec -n vf-test7 pod0 -- ip addr show net1
   ```

5. Test network connectivity:
   ```bash
   # Test cluster networking (eth0)
   kubectl exec -n vf-test7 pod0 -- ping kubernetes.default.svc.cluster.local
   
   # Test SR-IOV interface (net1) - requires target IP
   kubectl exec -n vf-test7 pod0 -- ping -I net1 <target-ip>
   ```

## Resource Lifecycle Management

### Reusing Claims
```bash
# Delete the pod but keep the ResourceClaim
kubectl delete pod pod0 -n vf-test7

# The ResourceClaim remains available
kubectl get resourceclaims -n vf-test7

# Create a new pod that uses the same claim
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  namespace: vf-test7
  name: pod1
spec:
  containers:
  - name: ctr0
    image: quay.io/schseba/toolbox:latest
    command: ["/bin/bash", "-c", "sleep INF"]
    resources:
      claims:
      - name: vf
  resourceClaims:
  - name: vf
    resourceClaimName: static-vf-claim
EOF
```

### Claim Status Monitoring
```bash
# Monitor claim status
kubectl get resourceclaims -n vf-test7 -w

# Detailed claim information
kubectl describe resourceclaim static-vf-claim -n vf-test7
```

## Performance Characteristics

- **Throughput**: Higher than standard pod networking
- **Latency**: Lower latency compared to virtual interfaces  
- **CPU Usage**: Efficient packet processing with SR-IOV acceleration
- **Compatibility**: Works with standard networking applications
- **Resource Control**: More explicit control over resource allocation timing

## Prerequisites

- SR-IOV capable network interface
- SR-IOV Network Operator installed and configured
- At least one Virtual Function available
- DRA-enabled Kubernetes cluster (v1.34+)
- Appropriate node labeling and device plugin setup

## Advantages of Static ResourceClaim

1. **Explicit Control**: Direct management of resource lifecycle
2. **Debugging**: Easier to debug resource allocation issues
3. **Resource Sharing**: Claims can be reused across different pods
4. **Administrative Separation**: Admins can pre-allocate resources
5. **Inspection**: Easier to inspect claim status and allocation details
6. **Testing**: Useful for development and testing scenarios

## Disadvantages of Static ResourceClaim

1. **Manual Management**: Requires explicit creation and cleanup
2. **Scaling Limitations**: Not suitable for dynamic scaling scenarios
3. **Complexity**: More complex for simple use cases
4. **Resource Waste**: Claims may remain allocated when not in use

## When to Use Each Approach

**Use ResourceClaimTemplate when:**
- Building production applications with scaling requirements
- Using Deployments, DaemonSets, or StatefulSets
- Need automatic resource lifecycle management
- Working with dynamic workloads

**Use ResourceClaim when:**
- Need explicit control over resource allocation
- Debugging resource allocation issues
- Testing and development scenarios
- Administrative control over resource distribution
- Sharing resources between different workloads over time

## Common Use Patterns

This static claim setup serves as foundation for:
- Resource allocation debugging and troubleshooting
- Administrative resource pre-allocation
- Development and testing environments
- Building custom resource management workflows
- Understanding DRA resource lifecycle mechanics

## Cleanup

Delete the demo Pods and direct claim before the demo-specific class and NAD.
Do not delete the full bundle: it also contains the potentially shared
`dra-driver-sriov/all-devices` policy.

```bash
kubectl delete pod pod0 pod1 -n vf-test7 --ignore-not-found --wait=true
kubectl delete resourceclaim static-vf-claim -n vf-test7 --ignore-not-found --wait=true
kubectl delete deviceclass demo-resourceclaim --ignore-not-found
kubectl delete network-attachment-definition demo-resourceclaim-net -n dra-driver-sriov --ignore-not-found
```

Remove `all-devices` only if this setup created it and no other workload depends
on it. Remove `vf-test7` only if this setup created the namespace and it is not
shared. Check whether these resources already exist before applying the demo;
preserve pre-existing or shared resources. Run each optional command only when
its ownership/dependency condition is satisfied:

```bash
kubectl delete sriovresourcepolicy all-devices -n dra-driver-sriov --ignore-not-found
kubectl delete namespace vf-test7 --ignore-not-found
```
