# Slingshot CXI Device Demo

This demo shows how to allocate a Slingshot Cassini (CXI) Virtual Function so the
workload gets both the network interface and the `/dev/cxiN` character device it
needs for RDMA traffic.

## Overview

Without the CXI character device the network interface is configured correctly but
RDMA traffic fails. The driver mounts it automatically, the same way it mounts
`/dev/infiniband/*` for RDMA-capable VFs.

## How it works

1. During discovery the driver checks `/sys/bus/pci/devices/<vf>/cxi/` for each VF
   and publishes a `sriovnetwork.k8snetworkplumbingwg.io/cxiCapable` boolean
   attribute in the ResourceSlice.
2. Workloads select CXI VFs with a CEL expression on that attribute instead of
   hard-coding Cassini PCI IDs.
3. During `NodePrepareResources` the driver resolves the single entry under the VF's
   `cxi` sysfs directory (for example `cxi4`), adds `/dev/cxi4` to the claim's CDI
   spec as a character device, and sets
   `SRIOVNETWORK_<DEVICE>_CXI_DEVICE=/dev/cxi4` in the container environment.

No CXI-specific opaque config is required — the mount is driven entirely by the
discovered attribute. The standard `VfConfig` opaque config is still used, since
`ifName` and `netAttachDefName` are what attach the SR-IOV network interface.

## Components

### SriovResourcePolicy
Opts Cassini VFs into advertisement, filtering on vendor `1590` and device `0372`.
Adjust the namespace to match the driver's Helm release namespace.

### DeviceClass
Selects only VFs that expose a CXI character device:

```
device.driver == 'sriovnetwork.k8snetworkplumbingwg.io' &&
device.attributes['sriovnetwork.k8snetworkplumbingwg.io'].cxiCapable
```

### NetworkAttachmentDefinition and ResourceClaimTemplate
Standard SR-IOV CNI setup, requesting one VF from the `cxi-vf` DeviceClass.

## Usage

1. Deploy the manifest:
   ```bash
   kubectl apply -f cxi-device.yaml
   ```
2. Confirm the VF was allocated and the character device is present:
   ```bash
   kubectl -n cxi-test exec cxi-pod -- ls -l /dev/cxi*
   kubectl -n cxi-test exec cxi-pod -- printenv | grep CXI_DEVICE
   kubectl -n cxi-test exec cxi-pod -- ip addr show hsn1
   ```
3. Verify the advertised attribute on the node:
   ```bash
   kubectl get resourceslices -o yaml | grep -A1 cxiCapable
   ```

## Prerequisites

- Slingshot Host Software with SR-IOV support, and the `cxi` driver loaded
- VFs created on the Cassini PF, each exposing an entry under
  `/sys/bus/pci/devices/<vf>/cxi/`
- The `sriov` CNI plugin installed on the node
