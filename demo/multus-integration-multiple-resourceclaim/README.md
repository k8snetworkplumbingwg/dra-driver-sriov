# Multus Integration with Multiple Resource Claims Demo

This demo attaches two independent SR-IOV claims to one pod, with each claim mapped to a different Multus network.

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

`multus-integration-multiple-resourceclaim.yaml` creates:

1. Namespace `vf-test9`
2. NAD `vf-test1` with `k8s.v1.cni.cncf.io/resourceName: sriov_nic_1`
3. NAD `vf-test2` with `k8s.v1.cni.cncf.io/resourceName: sriov_nic_2`
4. `ResourceClaimTemplate` `vf-test9` requesting one device per claim
5. Deployment `pod0`
   - Requests two independent claims (`sriov1` and `sriov2`)
   - Uses Multus annotation `vf-test1,vf-test2`

## Deploy

```bash
kubectl apply -f multus-integration-multiple-resourceclaim.yaml
```

## Verify

1. Check resources:

   ```bash
   kubectl get net-attach-def,resourceclaimtemplate,resourceclaim,deployment,pod -n vf-test9
   ```

2. Confirm both claims are present:

   ```bash
   kubectl get resourceclaim -n vf-test9
   ```

3. Verify two SR-IOV interfaces:

   ```bash
   POD_NAME=$(kubectl get pods -n vf-test9 -l app=pod0 -o jsonpath='{.items[0].metadata.name}')
   kubectl exec -n vf-test9 "$POD_NAME" -- ip link show
   kubectl exec -n vf-test9 "$POD_NAME" -- ip addr show net1
   kubectl exec -n vf-test9 "$POD_NAME" -- ip addr show net2
   ```

4. Check Multus status annotation:

   ```bash
   kubectl get pod -n vf-test9 "$POD_NAME" -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
   ```

## Troubleshooting

- **One claim binds, one fails**
  - Check that both resource names are published by policy/attributes
  - Verify VF capacity for both pools
- **Wrong claim-to-network mapping**
  - Ensure annotation order matches expected interface order: `vf-test1,vf-test2`
  - Confirm NAD resource names match device attributes
- **No secondary interface**
  - Verify Multus installation and SR-IOV CNI availability on the node
  - Inspect pod events and `ResourceClaim` events

## Cleanup

```bash
kubectl delete -f multus-integration-multiple-resourceclaim.yaml
```

## Related demos

- `../multus-integration-single-vf`
- `../multus-integration-multiple-vf`
