# Automatic Multus attachments POC

The driver reads the allocated VF's network template, creates a Pod-owned NAD
with that VF's `deviceID`, and adds the temporary NAD to the Pod's Multus network
selection. The submitted Pods have no `k8s.v1.cni.cncf.io/networks` annotation.
Source NADs have neither `resourceName` annotations nor `deviceID` fields.
Multus runs SR-IOV CNI and IPAM; the driver never calls CNI in MULTUS mode.

Enable `kubeletPlugin.configurationMode=MULTUS` and
`kubeletPlugin.multusAutoAttach=true` in the driver's Helm values. The equivalent
driver option is `--multus-auto-attach` / `MULTUS_AUTO_ATTACH=true`.
The redeploy target preserves existing Helm values:

```bash
rrun 'make redeploy-dra-driver-virtual-cluster'
```

Thick Multus must include the live-read implementation and have
`liveNetworkConfigOnAdd: true` in its daemon configuration. This POC was deployed
with the existing implementation from the sibling `multus-cni.v4` checkout.

## Run the two tests

```bash
export KUBECONFIG=~/kubeconfig/dra
python3 test/e2e/multus_auto_attach.py
```

The tests use only Python's standard library and `kubectl`. They leave the Pods
and their temporary NADs running in `dra-multus-poc` for inspection.

1. `workload1` uses a DeviceClass's `VfConfig.netAttachDefName` to obtain one VF
   and a secondary interface with a host-local address in `10.30.0.0/24`.
2. `workload2` requests two fake GPUs and two VFs. Pair-scoped `matchAttribute`
   constraints match each GPU/NIC's discovered PCIe root; `distinctAttribute`
   requires the pairs to use different roots. The allocated VF's attributes
   select `rail-a` or `rail-b`, preserving their separate IPAM subnets.

The second fixture replaces the first fixture's POC policy. Its mappings match
this virtual cluster's `eth1` and `eth0` PFs; adjust those names on other nodes.
The test checks each interface's actual PCI address through sysfs, its IP
subnet, unchanged source NADs, and the allocation's GPU/NIC topology.

```bash
kubectl -n dra-multus-poc get pod,resourceclaim,net-attach-def
kubectl -n dra-multus-poc exec workload2 -- ip -br addr
```

Verified on 22 October 2026 against the single-node Kubernetes v1.36.1 cluster:
both tests passed and both Pods remained Running.

| Pod | Interface | Allocated PCI | IP address | GPU/NIC pair root |
| --- | --- | --- | --- | --- |
| workload1 | net1 | 0000:15:10.2 | 10.30.0.2 | — |
| workload2 | net1 | 0000:15:10.6 | 192.0.2.3 | pci0000:14 |
| workload2 | net2 | 0000:29:10.2 | 198.51.100.3 | pci0000:28 |

For a fresh test run, delete the POC workloads and policy resources first:

```bash
kubectl delete -f demo/multus-auto-attach/gpu-nic-pairs.yaml --ignore-not-found
kubectl delete -f demo/multus-auto-attach/single-vf.yaml --ignore-not-found
```

## POC boundaries

Only single-plugin `sriov` NADs and exclusive kernel/netdevice VFs are supported.
For multiple VFs, omit `VfConfig.ifName` so Multus assigns distinct names, or
provide a different name per request. A NAD reference can come from `VfConfig`
or the allocated device's `sriovnetwork.k8snetworkplumbingwg.io/netAttachDefName`
and optional `netAttachDefNamespace` attributes. An omitted namespace means the
Pod namespace; conflicting references fail preparation.

This single-node test uses host-local IPAM and fake GPUs. It does not validate
physical GPU performance, multi-node IPAM, or communication between rails.
Recovery workers, protection finalizers, admission guards, conflists, and
operator changes from the full design are omitted. Normal unprepare deletes
the generated NADs, and Pod ownership also permits garbage collection. Keep
Multus's delegate cache available until CNI DEL finishes.
