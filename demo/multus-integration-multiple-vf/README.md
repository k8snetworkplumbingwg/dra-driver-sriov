# Multus Integration with Multiple Virtual Functions Demo

This demo allocates two SR-IOV Virtual Functions from a single `ResourceClaimTemplate` and attaches both through Multus.

## Prerequisite: deploy DRA driver in MULTUS mode

These Multus demos require the DRA driver to run with `kubeletPlugin.configurationMode=MULTUS`.

From the repository root:

```bash
helm upgrade -i dra-driver-sriov ./deployments/helm/dra-driver-sriov \
  --create-namespace -n dra-driver-sriov \
  --set kubeletPlugin.configurationMode=MULTUS
```

## Required Multus attributes (resource.k8s.io/v1)

Based on the Multus DRA integration update in [multus-cni PR #1492](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1492), each allocated device used by Multus must expose:
<!-- TODO: Remove this PR reference after multus-cni PR #1492 is merged. -->

- `k8s.cni.cncf.io/resourceName`
  - Must exactly match the NAD annotation `k8s.v1.cni.cncf.io/resourceName`
- `k8s.cni.cncf.io/deviceID`
  - Multus passes this value to the SR-IOV CNI plugin

## What this manifest creates

`multus-integration-multiple-vf.yaml` creates:

1. Namespace `vf-test8`
2. NAD `vf-test1` with `k8s.v1.cni.cncf.io/resourceName: sriov_nic_1`
3. `ResourceClaimTemplate` `vf-test8`
   - Requests `count: 2` from device class `sriovnetwork.openshift.io`
4. Deployment `pod0`
   - Uses Multus networks annotation `vf-test1,vf-test1`
   - Consumes a single claim named `sriov`

## Deploy

```bash
kubectl apply -f multus-integration-multiple-vf.yaml
```

## Verify

1. Check resources:

   ```bash
   kubectl get net-attach-def,resourceclaimtemplate,resourceclaim,deployment,pod -n vf-test8
   ```

2. Verify two interfaces in the pod:

   ```bash
   POD_NAME=$(kubectl get pods -n vf-test8 -l app=pod0 -o jsonpath='{.items[0].metadata.name}')
   kubectl exec -n vf-test8 "$POD_NAME" -- ip link show
   kubectl exec -n vf-test8 "$POD_NAME" -- ip addr show net1
   kubectl exec -n vf-test8 "$POD_NAME" -- ip addr show net2
   ```

3. Check Multus status annotation:

   ```bash
   kubectl get pod -n vf-test8 "$POD_NAME" -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
   ```

## Troubleshooting

- **Only one VF appears**
  - Confirm claim allocation includes two devices
  - Confirm pod annotation repeats the NAD twice (`vf-test1,vf-test1`)
- **No secondary interfaces**
  - Ensure NAD `resourceName` equals device `k8s.cni.cncf.io/resourceName`
  - Verify Multus and SR-IOV CNI on the worker node
- **Pod stays pending**
  - Check VF availability on target nodes
  - Inspect `ResourceClaim` events and DRA logs

## Cleanup

```bash
kubectl delete -f multus-integration-multiple-vf.yaml
```

## Related demos

- `../multus-integration-single-vf`
- `../multus-integration-multiple-resourceclaim`
