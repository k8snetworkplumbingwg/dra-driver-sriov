# Resource Alignment Demo (MULTUS)

This demo schedules a pod that requests an SR-IOV VF and a fake GPU with a
cross-driver `matchAttribute` on `resource.kubernetes.io/pcieRoot`. The VF is
attached with **Multus** (pod network annotation and DRA `resourceName` attributes).

For the same alignment constraint over the **NRI / VfConfig** path, see
[`../resource-alignment/`](../resource-alignment/).

DeviceClass `demo-multus-alignment` is administrator-owned; workloads contain no SR-IOV
opaque configuration. Applying the bundle requires permission to manage
its classes, policies, and NADs; the `dra-driver-sriov` namespace must exist.
Multus must allow references to NADs in that namespace.

## Prerequisite: deploy DRA SR-IOV driver in MULTUS mode

These Multus demos require `kubeletPlugin.configurationMode=MULTUS`.

Limitation: In `MULTUS` mode, the runtime DRA device metadata update path is not
active, so KEP-5304 DRA device metadata via CDI-mounted files is not supported
in this mode.

From the repository root on a virtual cluster:

```bash
make deploy-single-node-virtual-cluster-multus
```

Or on an existing cluster (STANDALONE or MULTUS SR-IOV driver plus Multus CNI):

```bash
export KUBECONFIG="${KUBECONFIG:-$HOME/.kcli/clusters/dra/auth/kubeconfig}"
make install-fake-gpu-driver
```

## Prerequisite: gpu.example.com fake GPU driver

Alignment needs a second DRA publisher that also exposes `resource.kubernetes.io/pcieRoot`
on ResourceSlice devices. This repo installs
[kubernetes-sigs/dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver)
via [`hack/install-dra-example-gpu-driver.sh`](../../hack/install-dra-example-gpu-driver.sh)
(pinned release **v0.5.0** image and Helm chart from `registry.k8s.io`, mirrored into the cluster registry).

Virtual-cluster deploy installs the fake GPU driver automatically (`GPU_PUBLISH_PCIE_ROOT=true`,
`PCIE_ROOTS` discovered from SR-IOV ResourceSlices). On an existing cluster, use
`make install-fake-gpu-driver`.

- Override roots manually if needed: `PCIE_ROOTS=pci0000:14,pci0000:28 make install-fake-gpu-driver`.
- To build from upstream git instead (e.g. `main`): `DRA_EXAMPLE_DRIVER_BUILD=1 DRA_EXAMPLE_DRIVER_REF=main make install-fake-gpu-driver`.

Fake GPUs assign `pcieRoot` in round-robin order from `PCIE_ROOTS`; values must
overlap what the SR-IOV driver publishes so `matchAttribute` can succeed.

## What this manifest creates

The `multus-integration-resource-alignment.yaml` file creates:

1. **`DeviceAttributes`** (`alignment-multus-attrs`) in namespace `dra-driver-sriov`
   - Publishes `k8s.cni.cncf.io/resourceName: sriov_nic_align`
2. **`SriovResourcePolicy`** (`alignment-multus-policy`) in namespace `dra-driver-sriov`
   - Selects the `DeviceAttributes` object by label and advertises matching devices
3. **Namespace** `vf-test10`
4. **NetworkAttachmentDefinition** `dra-driver-sriov/demo-multus-alignment-net`
   - Annotation `k8s.v1.cni.cncf.io/resourceName: sriov_nic_align`
   - SR-IOV CNI config with IPAM subnet `10.0.10.0/24`
5. **`ResourceClaimTemplate`** `resource-alignment-multus`
   - Requests one VF and one GPU with **`matchAttribute`**: `resource.kubernetes.io/pcieRoot`
   - No `VfConfig` opaque config (Multus attaches the VF)
6. **Pod** `pod0`
   - Annotation `k8s.v1.cni.cncf.io/networks: dra-driver-sriov/demo-multus-alignment-net`
   - Consumes the claim via `resourceClaimTemplateName: resource-alignment-multus`

## Required Multus attributes (resource.k8s.io/v1)

Based on the Multus DRA integration update in
[multus-cni PR #1492](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1492),
each allocated device used by Multus must expose:
<!-- TODO: Remove this PR reference after multus-cni PR #1492 is merged. -->

- `k8s.cni.cncf.io/resourceName` — must match the NAD annotation
  `k8s.v1.cni.cncf.io/resourceName`
- `k8s.cni.cncf.io/deviceID` — passed to the SR-IOV CNI plugin

This demo uses `sriov_nic_align` on both the `DeviceAttributes` and the NAD.

## Deploy

```bash
kubectl apply -f multus-integration-resource-alignment.yaml
```

## Verify

1. Confirm both drivers publish overlapping `pcieRoot` values:

   ```bash
   kubectl get resourceslices -o yaml | grep -E 'gpu.example.com|pcieRoot|sriovnetwork'
   ```

2. Check policy, attributes, and namespace resources:

   ```bash
   kubectl get deviceattributes,sriovresourcepolicy -n dra-driver-sriov
   kubectl get resourceclaimtemplate,resourceclaim,pod -n vf-test10
   kubectl get net-attach-def -n dra-driver-sriov demo-multus-alignment-net
   ```

3. Wait for the pod and inspect interfaces:

   ```bash
   kubectl wait --for=condition=Ready pod/pod0 -n vf-test10 --timeout=5m
   kubectl exec -n vf-test10 pod0 -- ip link show
   kubectl exec -n vf-test10 pod0 -- ip addr show net1
   ```

4. Inspect Multus network status annotation:

   ```bash
   kubectl get pod -n vf-test10 pod0 -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
   ```

## Run e2e

```bash
export KUBECONFIG="${KUBECONFIG:-$HOME/.kcli/clusters/dra/auth/kubeconfig}"
make e2e-workloads E2E_LABEL_FILTER='Multus && Alignment'
```

## Troubleshooting

- **Alignment claim not allocated**
  - Confirm `gpu.example.com` devices have `resource.kubernetes.io/pcieRoot` and values
    overlap SR-IOV VF attributes (`PCIE_ROOTS` / `make install-fake-gpu-driver`).
- **Pod pending on claim**
  - Verify `alignment-multus-policy` advertises devices; check DRA driver logs.
- **No `net1` interface in pod**
  - Confirm NAD `resourceName` equals device `k8s.cni.cncf.io/resourceName`.
  - Verify Multus and the SR-IOV CNI plugin are installed on the node.

## Cleanup

Delete the workload before removing the administrator-owned class and NAD.
Deleting only the workload namespace leaves those resources behind.

```bash
kubectl delete pod pod0 -n vf-test10 --ignore-not-found --wait=true
kubectl delete -f multus-integration-resource-alignment.yaml --ignore-not-found
```

## Related demos

- `../resource-alignment` — same `matchAttribute`, STANDALONE / NRI
- `../multus-integration-single-vf` — Multus VF without GPU alignment
