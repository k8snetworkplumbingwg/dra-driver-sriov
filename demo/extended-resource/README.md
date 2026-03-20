# Extended Resource Allocation Demo

This demo demonstrates the **DRAExtendedResource** feature gate (Kubernetes v1.34 alpha), which allows pods to request DRA-managed devices through standard extended resource requests — the same mechanism used by the legacy device plugin API — without creating explicit ResourceClaims.

## Overview

This scenario demonstrates:
- Requesting DRA devices via standard `resources.requests` instead of explicit ResourceClaims
- Using a dual-port SR-IOV setup with separate DeviceClasses per physical port
- Transparent ResourceClaim auto-creation by the scheduler
- Drop-in replacement for device plugin based workloads

With `DRAExtendedResource` enabled, there are two ways to request DRA devices via extended resources:

| Approach | Extended Resource Name | DeviceClass Requirement |
|----------|----------------------|------------------------|
| **Custom extendedResourceName** | Any name, e.g. `example.com/sriov-port1` | DeviceClass must set `spec.extendedResourceName` |
| **deviceclass prefix** | `deviceclass.resource.kubernetes.io/<class-name>` | Works with any DeviceClass (no changes needed) |

In both cases, the scheduler transparently creates a ResourceClaim with an `ExactCount` matching the requested quantity.

## Components

### 1. SriovResourcePolicy (Dual-Port)
The `SriovResourcePolicy` defines two resource groups — one per physical NIC port:
- **port1-vfs**: VFs on PCI bus `08:00.{2,3,4,5}`
- **port2-vfs**: VFs on PCI bus `08:02.{2,3,4,5}`

### 2. DeviceClasses
Two DeviceClasses define per-port extended resources:
- **sriov-port1**: Selects `port1-vfs` devices, exposes `example.com/sriov-port1`
- **sriov-port2**: Selects `port2-vfs` devices, exposes `example.com/sriov-port2`

Each DeviceClass includes:
- **selectors**: CEL expression filtering by driver and resourceName
- **config**: Opaque VfConfig referencing a per-port NetworkAttachmentDefinition
- **extendedResourceName**: The extended resource name pods will request

### 3. NetworkAttachmentDefinitions
Two NetworkAttachmentDefinitions (`sriov-port1-net`, `sriov-port2-net`) provide SR-IOV CNI configuration for each port.

### 4. Pod Deployment
Two test pods demonstrate extended resource requests:
- **ext-resource-dual-port**: Requests 1 VF from each port (2 VFs total)
- **ext-resource-dual-port-2x2**: Requests 2 VFs from each port (4 VFs total)

Pods use standard `resources.requests` / `resources.limits` — no `resourceClaims` or `ResourceClaimTemplate` needed.

## Architecture

```
                    deviceclass.yaml
┌──────────────────────────────────────────────────────────┐
│                                                          │
│  SriovResourcePolicy "dual-port-vfs"                     │
│  ┌───────────────────┐  ┌───────────────────┐            │
│  │ port1-vfs         │  │ port2-vfs         │            │
│  │ 08:00.{2,3,4,5}   │  │ 08:02.{2,3,4,5}   │            │
│  └────────┬──────────┘  └────────┬──────────┘            │
│           │                      │                       │
│  DeviceClass "sriov-port1"    DeviceClass "sriov-port2"  │
│  extendedResourceName:        extendedResourceName:      │
│  example.com/sriov-port1      example.com/sriov-port2    │
│  config → sriov-port1-net     config → sriov-port2-net   │
│           │                      │                       │
│  NetworkAttachmentDefinition  NetworkAttachmentDefinition│
│  "sriov-port1-net"            "sriov-port2-net"          │
└──────────────────────────────────────────────────────────┘

                pod-extended-resource.yaml
┌─────────────────────────────────────────────────────────┐
│  Pod "ext-resource-dual-port"                           │
│  requests:                                              │
│    example.com/sriov-port1: "1"   → vfnet0 (port 1 VF)  │
│    example.com/sriov-port2: "1"   → vfnet1 (port 2 VF)  │
│                                                         │
│  Pod "ext-resource-dual-port-2x2"                       │
│  requests:                                              │
│    example.com/sriov-port1: "2"   → vfnet0, vfnet1      │
│    example.com/sriov-port2: "2"   → vfnet2, vfnet3      │
└─────────────────────────────────────────────────────────┘
```

## Use Cases

Extended resource allocation is ideal for:
- **Device Plugin Migration**: Migrating workloads from device plugin to DRA without changing pod specs
- **Simple Device Allocation**: Auto-assigned interface names without explicit ResourceClaims
- **Mixed Clusters**: Clusters where some nodes use device plugin and others use DRA
- **Per-Port Selection**: Separate DeviceClasses for different physical NIC ports

## Usage

1. Deploy the DeviceClasses, SriovResourcePolicy, and NetworkAttachmentDefinitions:
   ```bash
   kubectl apply -f deviceclass.yaml
   ```

2. Verify the DeviceClasses:
   ```bash
   kubectl get deviceclass sriov-port1 sriov-port2
   ```

3. Deploy the test pods:
   ```bash
   kubectl apply -f pod-extended-resource.yaml
   ```

4. Wait for pods to be ready:
   ```bash
   kubectl wait --for=condition=ready pod -l app=ext-resource-test --timeout=120s
   ```

5. Verify pods and auto-created ResourceClaims:
   ```bash
   kubectl get pods -l app=ext-resource-test -o wide
   kubectl get resourceclaims -n default
   ```

6. Inspect the auto-generated ResourceClaim:
   ```bash
   kubectl get resourceclaims -o yaml
   ```

7. Check VF interfaces inside the pods:
   ```bash
   # Dual-port pod (1+1 = 2 VFs)
   kubectl exec ext-resource-dual-port -- ip link show

   # 2x2 pod (2+2 = 4 VFs)
   kubectl exec ext-resource-dual-port-2x2 -- ip link show
   ```

### Alternative: deviceclass.resource.kubernetes.io/ Prefix

Instead of defining `extendedResourceName` on each DeviceClass, you can use the well-known prefix:

```yaml
resources:
  requests:
    deviceclass.resource.kubernetes.io/sriov-port1: "1"
    deviceclass.resource.kubernetes.io/sriov-port2: "1"
  limits:
    deviceclass.resource.kubernetes.io/sriov-port1: "1"
    deviceclass.resource.kubernetes.io/sriov-port2: "1"
```

This works with any DeviceClass without modifications.

## Comparison with Explicit ResourceClaims

The other demos in this repository (`single-vf-claim/`, `resourceclaim/`, `vfio-driver/`) use explicit ResourceClaims with opaque driver config.

| Feature | Extended Resource | Explicit ResourceClaim |
|---------|-------------------|----------------------|
| Pod spec complexity | Minimal — just `resources.requests` | Requires `resourceClaims` + `ResourceClaimTemplate` |
| Device allocation | Yes | Yes |
| Per-port device selection | Yes (one DeviceClass per port) | Yes (CEL selectors per request) |
| CNI config (NetworkAttachmentDefinition) | Yes (via DeviceClass config) | Yes (via opaque config per request) |
| Custom interface naming (ifName) | No — see note below | Yes (unique ifName per request) |
| Multi-device request | Yes (set count > 1) | Yes (set count in `exactly`) |
| Migration from device plugin | Drop-in replacement | Requires pod spec changes |

> **Note on ifName:** DeviceClass config applies uniformly to all allocated devices of that class. If `ifName: net1` is set and a pod requests 2+ VFs from the same DeviceClass, both try to use `net1`, causing a sandbox creation loop. Omit `ifName` and let the driver auto-assign unique names (`vfnet0`, `vfnet1`, ...). Use explicit ResourceClaims if you need specific interface names per device.

## Customization

### Changing PCI Addresses

Update the `SriovResourcePolicy` in `deviceclass.yaml` with your actual VF PCI addresses:

```bash
# Find VF PCI addresses on your host
ls -la /sys/class/net/eth2/device/virtfn*/
ls -la /sys/class/net/eth3/device/virtfn*/
```

### Using Different Extended Resource Names

Edit `extendedResourceName` in each DeviceClass and update the pod specs accordingly:

```yaml
# In DeviceClass
spec:
  extendedResourceName: mycompany.com/port1-nic

# In pod spec
resources:
  requests:
    mycompany.com/port1-nic: "1"
  limits:
    mycompany.com/port1-nic: "1"
```

## Cleanup

```bash
kubectl delete -f pod-extended-resource.yaml --ignore-not-found
kubectl delete -f deviceclass.yaml
```

## Prerequisites

- Kubernetes 1.34+ cluster with DRA support
- **DRAExtendedResource** feature gate enabled on kube-apiserver, kube-scheduler, and kubelet
- DRA SR-IOV driver deployed
- SR-IOV VFs created on the host (dual-port NIC with VFs on both ports)

## Troubleshooting

### Pod stuck in Pending with "Unresolvable extended resource"

The `DRAExtendedResource` feature gate is not enabled. Verify:

```bash
kubectl get pod -n kube-system -l component=kube-apiserver \
  -o jsonpath='{.items[0].spec.containers[0].command}' | tr ',' '\n' | grep DRAExtendedResource

kubectl get pod -n kube-system -l component=kube-scheduler \
  -o jsonpath='{.items[0].spec.containers[0].command}' | tr ',' '\n' | grep DRAExtendedResource
```

### Pod stuck in Pending with "Insufficient example.com/sriov-port1"

No devices match the DeviceClass selectors. Check:

```bash
kubectl get resourceslices
kubectl get deviceclass sriov-port1 sriov-port2 -o yaml
```

### Multi-VF pod stuck in ContainerCreating

If the DeviceClass config specifies `ifName`, all VFs of that class try to use the same interface name. Remove `ifName` from the DeviceClass config — the driver will auto-assign unique names (`vfnet0`, `vfnet1`, ...).
