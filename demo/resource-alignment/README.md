# Resource Alignment Demo (STANDALONE)

This demo schedules a pod that requests an SR-IOV VF and a fake GPU with a
cross-driver `matchAttribute` on `resource.kubernetes.io/pcieRoot`. The VF is
attached through the driver **NRI** path (`VfConfig`: `ifName`, `netAttachDefName`).

For the same alignment constraint with **Multus** attach, see
[`../multus-integration-resource-alignment/`](../multus-integration-resource-alignment/).

DeviceClass `demo-resource-alignment` is administrator-owned; workloads contain no SR-IOV
opaque configuration. Applying the bundle requires permission to manage
its classes, policies, and NADs; the `dra-driver-sriov` namespace must exist.
Run the driver in STANDALONE mode with kernel-bound VFs.

## Prerequisite: deploy DRA SR-IOV driver in STANDALONE mode

This fixture uses opaque `VfConfig` on DeviceClass `demo-resource-alignment`. The claim contains requests and constraints only. The driver must run with
`kubeletPlugin.configurationMode=STANDALONE` (NRI enabled).

From the repository root on a virtual cluster:

```bash
make deploy-single-node-virtual-cluster-standalone
```

Or on an existing cluster:

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

The `resource-alignment.yaml` file creates:

1. **`SriovResourcePolicy`** (`all-devices`) in namespace `dra-driver-sriov`
   - Advertises all discovered SR-IOV VFs using an empty policy config
2. **Namespace** `vf-test5`
3. **NetworkAttachmentDefinition** `demo-alignment-net` in namespace `dra-driver-sriov`
   - SR-IOV CNI config with IPAM subnet `10.0.1.0/24`
4. **`DeviceClass`** `demo-resource-alignment`
   - Selects VFs from `sriovnetwork.k8snetworkplumbingwg.io`
   - **`VfConfig`**: `ifName: net1`, `netAttachDefName: demo-alignment-net`
   - **`netAttachDefNamespace`**: `dra-driver-sriov`
5. **`ResourceClaimTemplate`** `resource-alignment`
   - Requests one VF (`demo-resource-alignment`) and one GPU (`gpu.example.com`)
   - **`matchAttribute`**: `resource.kubernetes.io/pcieRoot`
   - References the administrator-managed class without claim-level `VfConfig`
6. **Pod** `pod0`
   - Consumes the claim via `resourceClaimTemplateName: resource-alignment`

## Deploy

```bash
kubectl apply -f resource-alignment.yaml
```

## Verify

1. Confirm both drivers publish overlapping `pcieRoot` values:

   ```bash
   kubectl get resourceslices -o yaml | grep -E 'gpu.example.com|pcieRoot|sriovnetwork'
   ```

2. Check namespace resources and claim allocation:

   ```bash
   kubectl get resourceclaimtemplate,resourceclaim,pod -n vf-test5
   kubectl get net-attach-def -n dra-driver-sriov demo-alignment-net
   ```

3. Wait for the pod and inspect interfaces:

   ```bash
   kubectl wait --for=condition=Ready pod/pod0 -n vf-test5 --timeout=5m
   kubectl exec -n vf-test5 pod0 -- ip link show
   kubectl exec -n vf-test5 pod0 -- ip addr show net1
   ```

## Run e2e

```bash
export KUBECONFIG="${KUBECONFIG:-$HOME/.kcli/clusters/dra/auth/kubeconfig}"
make e2e-workloads E2E_LABEL_FILTER='Standalone && Alignment'
```

## Troubleshooting

- **Alignment claim not allocated**
  - Confirm `gpu.example.com` devices have `resource.kubernetes.io/pcieRoot` and values
    overlap SR-IOV VF attributes (`PCIE_ROOTS` / `make install-fake-gpu-driver`).
- **Pod pending**
  - Check `ResourceClaim` events; ensure `all-devices` policy is applied and VFs exist.
- **No `net1` in pod**
  - Driver must be STANDALONE; verify the DeviceClass VfConfig references the protected NAD namespace.

## Cleanup

Delete the demo-owned workload, template, class and NAD explicitly. Do not delete
the full bundle, which also contains the potentially shared discovery policy.

```bash
(
  set -eu
  kubectl delete pod pod0 -n vf-test5 --ignore-not-found --wait=true
  kubectl delete resourceclaimtemplate resource-alignment -n vf-test5 --ignore-not-found
  kubectl delete deviceclass demo-resource-alignment --ignore-not-found
  kubectl delete network-attachment-definition demo-alignment-net -n dra-driver-sriov --ignore-not-found
)
```

Before setup, record whether `dra-driver-sriov/all-devices` and namespace
`vf-test5` already exist. Remove the policy only if this demo created it and no
other workload depends on it. Remove the namespace only if this demo created it,
it is not shared, and its generated claims have finished cleanup. Run each
optional command only when its condition is satisfied:

```bash
kubectl delete sriovresourcepolicy all-devices -n dra-driver-sriov --ignore-not-found
kubectl delete namespace vf-test5 --ignore-not-found
```

## Related demos

- `../multus-integration-resource-alignment` — same `matchAttribute`, Multus attach
- `../single-vf-claim` — STANDALONE VF without GPU alignment
