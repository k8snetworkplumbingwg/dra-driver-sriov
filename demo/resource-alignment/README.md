# Resource Alignment Demo

This demo demonstrates how to request multiple device types (SR-IOV Virtual Functions and GPUs) with NUMA node alignment constraints to ensure optimal performance through locality.

## Overview

This scenario showcases:
- Multi-device resource claims combining SR-IOV VF and GPU resources
- NUMA node alignment using resource constraints to ensure devices are co-located
- Cross-driver resource coordination between SR-IOV DRA driver and GPU DRA driver
- Performance optimization through hardware topology awareness

## Components

### 1. Networking Setup
- Creates dedicated namespace (`vf-test5`)
- NetworkAttachmentDefinition with SR-IOV CNI plugin configuration
- IPAM setup using host-local plugin with subnet `10.0.1.0/24`
- Standard SR-IOV network settings (VLAN 0, spoofchk on, trust on)

### 2. Multi-Device Resource Claim
The `ResourceClaimTemplate` requests resources from two different device classes:
- **SR-IOV VF**: `deviceClassName: sriovnetwork.k8snetworkplumbingwg.io`
- **GPU Device**: `deviceClassName: gpu.example.com` (count: 1)
- **Alignment Constraint**: `matchAttribute: "resource.kubernetes.io/numaNode"`
  - Ensures both VF and GPU are allocated from the same NUMA node
  - Optimizes memory access patterns and reduces cross-NUMA traffic
  - Improves overall application performance

### 3. Device Configuration
- **VfConfig parameters**:
  - `ifName: net1`: Network interface name in the container  
  - `netAttachDefName: vf-test1`: References the NetworkAttachmentDefinition
  - Standard kernel driver binding for networking

### 4. Pod Deployment
- Toolbox container with elevated privileges for testing and validation
- Resource claim binding for both VF and GPU devices
- Network capabilities (NET_ADMIN, NET_RAW) for interface management
- Privileged execution context for device access

## Use Cases

Resource alignment is critical for:
- **ML/AI Workloads**: GPU compute with high-performance networking for distributed training
- **HPC Applications**: Scientific computing requiring both acceleration and fast interconnects
- **Real-time Processing**: Low-latency applications needing optimal NUMA locality
- **Data Analytics**: High-throughput data ingestion with GPU acceleration
- **Edge Computing**: Resource-constrained environments requiring optimal device placement

## NUMA Alignment Benefits

| Aspect | Aligned Resources | Non-Aligned Resources |
|--------|------------------|----------------------|
| **Memory Bandwidth** | Higher (local access) | Lower (cross-NUMA) |
| **Latency** | Lower | Higher |
| **CPU Efficiency** | Better | Reduced |
| **Power Consumption** | Lower | Higher |
| **Overall Performance** | Optimal | Suboptimal |

## Resource Allocation Process

1. **Multi-Device Request**: Pod requests both VF and GPU with alignment constraint
2. **Topology Discovery**: DRA drivers analyze NUMA topology on target nodes  
3. **Constraint Evaluation**: System finds devices on the same NUMA node
4. **Coordinated Allocation**: Both drivers allocate devices from matching NUMA domain
5. **Interface Setup**: SR-IOV CNI creates network interface in pod
6. **GPU Preparation**: GPU driver prepares device access (environment variables in example driver)
7. **Pod Scheduling**: Pod starts with optimally placed resources

## Usage

### Prerequisites

Before running this demo, you need to install the DRA example driver

https://github.com/kubernetes-sigs/dra-example-driver

And switch the image after the deployment to `quay.io/schseba/dra-example-driver:latest`

### Running the Demo

1. Deploy the resource alignment configuration:
   ```bash
   kubectl apply -f resource-alignment.yaml
   ```

2. Verify pod deployment and resource allocation:
   ```bash
   kubectl get pods -n vf-test5
   kubectl describe pod pod0 -n vf-test5
   ```

3. Check resource claims and allocation:
   ```bash
   kubectl get resourceclaim -n vf-test5
   kubectl describe resourceclaim -n vf-test5
   ```

4. Validate GPU environment variables (example driver sets these):
   ```bash
   kubectl exec -n vf-test5 pod0 -- env | grep GPU_DEVICE
   ```

5. Test network functionality:
   ```bash
   # Check network interfaces
   kubectl exec -n vf-test5 pod0 -- ip link show
   kubectl exec -n vf-test5 pod0 -- ip addr show net1
   ```

## Performance Considerations

- **NUMA Locality**: Resources on the same NUMA node provide optimal memory access
- **Cross-NUMA Traffic**: Avoided through proper constraint configuration
- **Memory Bandwidth**: Maximized by keeping compute and I/O resources local
- **Application Design**: Applications should be NUMA-aware to fully benefit

## Troubleshooting

### Common Issues

1. **No suitable devices found**: 
   - Check NUMA topology: `kubectl describe nodes`
   - Verify both SR-IOV and GPU resources exist on the same NUMA domain

2. **Resource allocation failures**:
   - Ensure sufficient VFs and GPUs available
   - Check device plugin status and resource advertisements

3. **Pod stuck in pending**:
   - Verify DRA example driver is running: `kubectl get pods -n kube-system | grep dra-example`
   - Check scheduler logs for constraint violations

### Verification Commands

```bash
# Monitor resource allocation
kubectl get events -n vf-test5 --sort-by='.lastTimestamp'
```

## Prerequisites

- Kubernetes cluster with DRA (Dynamic Resource Allocation) support (v1.34+)
- DRA example driver installed with image `quay.io/schseba/dra-example-driver:latest`
- SR-IOV capable hardware with Virtual Functions configured
- SR-IOV Network Operator installed and configured  
- NUMA-aware hardware topology
- Multiple NUMA nodes with both SR-IOV and GPU resources
- Appropriate node labeling and resource advertisements

## Advanced Configuration

For production deployments, consider:
- **Resource Policies**: Define organization-wide alignment policies
- **Quality of Service**: Set appropriate QoS classes for critical workloads  
- **Monitoring**: Implement NUMA-aware performance monitoring
- **Tuning**: Configure application for optimal NUMA utilization
