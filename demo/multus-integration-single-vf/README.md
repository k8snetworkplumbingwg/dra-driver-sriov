# Multus Integration with Single Virtual Function Demo

This demo shows how one SR-IOV Virtual Function (VF) is allocated with DRA and attached by Multus to a workload.

## Prerequisite: deploy DRA driver in MULTUS mode

These Multus demos require the DRA driver to run with `kubeletPlugin.configurationMode=MULTUS`.

From the repository root:

```bash
helm upgrade -i dra-driver-sriov ./deployments/helm/dra-driver-sriov \
  --create-namespace -n dra-driver-sriov \
  --set kubeletPlugin.configurationMode=MULTUS
```

## What this manifest creates

The `multus-integration-single-vf.yaml` file creates:

1. **`DeviceAttributes`** (`sriov-nic-1-attrs`) in namespace `dra-driver-sriov`
   - Publishes `k8s.cni.cncf.io/resourceName: sriov_nic_1`
2. **`SriovResourcePolicy`** (`sriov-nic-1-policy`) in namespace `dra-driver-sriov`
   - Selects the `DeviceAttributes` object by label and advertises matching devices
3. **Namespace** `vf-test7`
4. **NetworkAttachmentDefinition** `vf-test1`
   - Uses annotation `k8s.v1.cni.cncf.io/resourceName: sriov_nic_1`
5. **`ResourceClaimTemplate`** `vf-test7`
   - Requests one device from class `sriovnetwork.k8snetworkplumbingwg.io`
6. **Deployment** `pod0`
   - Consumes the claim and attaches network `vf-test1` via Multus

## Required Multus attributes (resource.k8s.io/v1)

Based on the Multus DRA integration update in [multus-cni PR #1492](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1492), each allocated device used by Multus must expose:
<!-- TODO: Remove this PR reference after multus-cni PR #1492 is merged. -->

- `k8s.cni.cncf.io/resourceName`
  - Must exactly match the NAD annotation `k8s.v1.cni.cncf.io/resourceName`
- `k8s.cni.cncf.io/deviceID`
  - Multus passes this value to the SR-IOV CNI plugin

In this demo, both sides use `sriov_nic_1`, which is the critical match for claim-to-NAD mapping.

## Deploy

```bash
kubectl apply -f multus-integration-single-vf.yaml
```

## Verify

1. Check policy and attributes:

   ```bash
   kubectl get deviceattributes,sriovresourcepolicy -n dra-driver-sriov
   kubectl describe deviceattributes sriov-nic-1-attrs -n dra-driver-sriov
   ```

2. Check namespace resources:

   ```bash
   kubectl get net-attach-def,resourceclaimtemplate,resourceclaim,deployment,pod -n vf-test7
   ```

3. Check pod interfaces:

   ```bash
   POD_NAME=$(kubectl get pods -n vf-test7 -l app=pod0 -o jsonpath='{.items[0].metadata.name}')
   kubectl exec -n vf-test7 "$POD_NAME" -- ip link show
   kubectl exec -n vf-test7 "$POD_NAME" -- ip addr show net1
   ```

4. Inspect Multus network status annotation:

   ```bash
   kubectl get pod -n vf-test7 "$POD_NAME" -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
   ```

## Troubleshooting

- **Pod pending on claim**
  - Verify a matching device is advertised by `SriovResourcePolicy`
  - Check `ResourceClaim` events and DRA driver logs
- **No `net1` interface in pod**
  - Confirm NAD annotation equals device `k8s.cni.cncf.io/resourceName`
  - Verify Multus and SR-IOV CNI plugin are installed on the node
- **Attachment created but traffic fails**
  - Validate NAD IPAM subnet and L2 reachability
  - Check interface state in the pod with `ip -d link show net1`

## Cleanup

```bash
kubectl delete -f multus-integration-single-vf.yaml
```

## Related demos

- `../multus-integration-multiple-vf`
- `../multus-integration-multiple-resourceclaim`
