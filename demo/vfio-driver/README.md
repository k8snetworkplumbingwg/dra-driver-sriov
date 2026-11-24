# VFIO Driver Configuration Demo

This demo illustrates how to configure SR-IOV Virtual Functions with VFIO-PCI driver for userspace applications like DPDK.

DeviceClass `vf-test2` owns the VFIO/vhost configuration; the claim template
contains requests only. Apply the bundle with permission to manage DeviceClasses
and policies in the existing `dra-driver-sriov` namespace.

## Overview

This scenario demonstrates:
- Configuring Virtual Functions to use VFIO-PCI driver instead of kernel networking drivers
- Setting up device passthrough for high-performance userspace networking applications
- Exposing optional vhost/tun device nodes for userspace networking

## Components

### 1. DeviceClass Configuration
The `DeviceClass` resource configures how devices should be allocated:
- **selectors**: Uses CEL to filter devices by the SR-IOV DRA driver
- **config**: Specifies VFIO-PCI driver configuration
- **VfConfig parameters**:
  - `driver: vfio-pci`: Binds VF to VFIO-PCI driver for userspace access
  - `addVhostMount: true`: Exposes `/dev/vhost-net` and `/dev/net/tun`

### 2. Networking Setup
- Creates dedicated namespace (`vf-test2`)
- Uses the Pod's primary network for management connectivity
- Omits the NAD fields and `ifName`; NRI skips secondary CNI attachment for this VFIO device

### 3. Resource Allocation
- ResourceClaimTemplate requests VF from the VFIO-configured DeviceClass
- The administrator DeviceClass supplies VFIO-PCI binding and vhost device exposure
- The claim template supplies no SR-IOV opaque configuration

### 4. Pod Deployment
- Deploys toolbox container for testing and development
- Claims VFIO-configured resource for high-performance networking
- Maintains network admin capabilities for testing

## Use Cases

This configuration is ideal for:
- **DPDK Applications**: High-performance packet processing in userspace
- **NFV Workloads**: Network Function Virtualization with hardware acceleration
- **Data Plane Applications**: Applications requiring direct hardware access
- **High-throughput Networking**: Bypassing kernel network stack for performance

## VFIO vs Kernel Driver

| Aspect | VFIO-PCI | Kernel Driver |
|--------|----------|---------------|
| **Performance** | Higher (userspace) | Lower (kernel overhead) |
| **CPU Usage** | Lower (poll mode) | Higher (interrupt driven) |
| **Latency** | Lower | Higher |
| **Application Type** | Userspace (DPDK) | Kernel networking |
| **Resource Requirements** | More memory | Standard |

## Usage

1. Deploy the administrator configuration and sample workload:
   ```bash
   kubectl apply -f vfio-driver-config.yaml
   ```

   For an existing `pod0` installation, use the migration sequence below instead
   of the fresh-install command. Existing Pods keep their generated claims, and
   ResourceClaimTemplate specs are immutable: delete the old Pod, wait for its
   generated claim to disappear, then recreate the template and apply the updated
   bundle. If the old Pod has no recorded claim name, inspect its claims before
   proceeding.

   ```bash
   old_claim=$(kubectl get pod pod0 -n vf-test2 \
     -o jsonpath='{.status.resourceClaimStatuses[?(@.name=="vf")].resourceClaimName}')
   (
     set -eu
     if [ -z "$old_claim" ]; then
       echo 'Inspect the old Pod and ResourceClaims before migration.' >&2
       exit 1
     fi
     printf 'Old ResourceClaim: %s\n' "$old_claim"
     kubectl delete pod pod0 -n vf-test2 --wait=true
     kubectl wait --for=delete "resourceclaim/$old_claim" -n vf-test2 --timeout=180s
     kubectl delete resourceclaimtemplate single-vf -n vf-test2 --ignore-not-found
     kubectl apply -f vfio-driver-config.yaml
   )
   ```

   Keep the printed claim name; `old_claim` also remains set in the calling shell.
   If deletion or waiting times out, resolve the cleanup problem and resume below
   without looking up the deleted Pod or rerunning the capture command. In a new
   shell, first set `old_claim='<printed claim name>'`. Template replacement and
   deployment run only after the claim is gone:

   ```bash
   (
     set -eu
     : "${old_claim:?Set old_claim to the saved ResourceClaim name}"
     kubectl wait --for=delete "resourceclaim/$old_claim" -n vf-test2 --timeout=180s
     kubectl delete resourceclaimtemplate single-vf -n vf-test2 --ignore-not-found
     kubectl apply -f vfio-driver-config.yaml
   )
   ```

2. The DRA driver will:
   - Bind the allocated VF to vfio-pci driver
   - Create device nodes in `/dev/vfio/`
   - Load the required modules and expose `/dev/vhost-net` and `/dev/net/tun` if enabled

3. Applications in the pod can access the VF through:
   - VFIO device files (`/dev/vfio/vfio`, `/dev/vfio/<group>`)
   - DPDK libraries and frameworks
   - Direct userspace networking APIs

## Prerequisites

- VFIO kernel modules loaded (`vfio-pci`, `vfio_iommu_type1`)
- IOMMU enabled in BIOS/UEFI
- Hugepages configured (typically required for DPDK)
- Container runtime with device passthrough support
- Appropriate security policies for device access
- Vhost/tun kernel support when `addVhostMount: true`

## Security Considerations

- VFIO provides secure device isolation through IOMMU
- Containers get direct hardware access - ensure trust boundaries
- Consider using device plugins or security contexts to limit access

## Cleanup

Delete only the demo-owned Pod, generated claim, template, and class. Do not
delete the whole bundle, which also contains the discovery policy and namespace:

```bash
(
  set -eu
  claim=$(kubectl get pod pod0 -n vf-test2 --ignore-not-found \
    -o jsonpath='{.status.resourceClaimStatuses[?(@.name=="vf")].resourceClaimName}')
  printf 'ResourceClaim: %s\n' "$claim"
  kubectl delete pod pod0 -n vf-test2 --ignore-not-found --wait=true
  if [ -n "$claim" ]; then
    kubectl wait --for=delete "resourceclaim/$claim" -n vf-test2 --timeout=180s
  fi
  kubectl delete resourceclaimtemplate single-vf -n vf-test2 --ignore-not-found
  kubectl delete deviceclass vf-test2 --ignore-not-found
)
```

The subshell stops on any error, including a claim-deletion timeout, even when
pasted into an interactive shell. Keep the printed claim name. If cleanup stops
after Pod deletion, resolve the problem, set `claim='<printed claim name>'`, and
run the remaining cleanup below. Do not rerun the Pod lookup, which can no longer
recover the claim name. Do not run the optional policy/namespace deletion until
this cleanup succeeds:

```bash
(
  set -eu
  : "${claim:?Set claim to the saved ResourceClaim name}"
  kubectl wait --for=delete "resourceclaim/$claim" -n vf-test2 --timeout=180s
  kubectl delete resourceclaimtemplate single-vf -n vf-test2 --ignore-not-found
  kubectl delete deviceclass vf-test2 --ignore-not-found
)
```

Before setup, record whether `dra-driver-sriov/all-devices` and namespace
`vf-test2` already exist. Remove the policy only if this demo created it and no
other workload depends on it; remove the namespace only if this demo created it
and it is not shared. Preserve unrelated or shared resources. Run each optional
command only when its condition is satisfied:

```bash
kubectl delete sriovresourcepolicy all-devices -n dra-driver-sriov --ignore-not-found
kubectl delete namespace vf-test2 --ignore-not-found
```
