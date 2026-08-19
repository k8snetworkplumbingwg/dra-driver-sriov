# Host-Device Kernel Flow Demo

This demo shows a kernel-driver SR-IOV flow using a `host-device` CNI `NetworkAttachmentDefinition` (NAD).

## What it demonstrates

- `VfConfig` with kernel networking (omit `driver` or set `driver: ""`)
- `host-device` NAD with `capabilities.deviceID=true`
- `netAttachDefName` usage required for kernel traffic in `STANDALONE`

## Apply

```bash
kubectl apply -f host-device-kernel.yaml
```

## Notes

- In `STANDALONE` mode, the DRA driver fetches the NAD config and performs CNI attach/detach via NRI.
- In `MULTUS` mode, this demo publishes `k8s.cni.cncf.io/resourceName: openshift.io/host-device-net` through a matching `DeviceAttributes` object, and the NAD advertises the same value with `k8s.v1.cni.cncf.io/resourceName: openshift.io/host-device-net`. Add a pod network annotation (for example, `k8s.v1.cni.cncf.io/networks: host-device-net`) and let Multus perform attachment.
