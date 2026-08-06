# Workload e2e tests

Go + Ginkgo suite that validates DRA SR-IOV advertising and applies YAML under
[`demo/`](../../demo/) as fixtures against a live cluster.

## Ownership

| Layer | Owns |
|-------|------|
| `make deploy-single-node-virtual-cluster-standalone` | Cluster, STANDALONE driver install, host VF creation, driver restart. Does **not** apply `SriovResourcePolicy` or wait for ResourceSlices. |
| `make deploy-single-node-virtual-cluster-multus` | Same as above with `DRA_DRIVER_MODE=MULTUS` (requires Multus CNI on the cluster for Multus demos). |
| `smoke/advertise-devices` | Catch-all `all-devices` policy => ResourceSlices publish devices; cleans up afterward. |
| `demo_*_test.go` | Each demo fixture applies (and cleans up) its own policies / workloads. |

## Prerequisites

1. A cluster with the DRA SR-IOV driver installed
   (for example `make deploy-single-node-virtual-cluster-standalone`,
   `make deploy-single-node-virtual-cluster-multus`, or
   `make deploy-virtual-k8s-cluster`).
2. `KUBECONFIG` pointing at that cluster (virtual deploy defaults to
   `~/.kcli/clusters/dra/auth/kubeconfig`).

## Run

```bash
export KUBECONFIG="${KUBECONFIG:-$HOME/.kcli/clusters/dra/auth/kubeconfig}"
make e2e-workloads
```

CI (`.github/workflows/virtual-e2e.yaml`) runs this suite in a matrix for both
`standalone` and `multus` driver modes. Mode-specific demos auto-skip based on
cluster detection (`SkipUnlessMultus` / `SkipUnlessStandalone`).

Filter by Ginkgo labels locally:

```bash
make e2e-workloads E2E_LABEL_FILTER='!Multus && !Alignment'
```

## Demo fixture contract

- YAML under `demo/` is the source of truth for both documentation and e2e.
- Fixtures are loaded **as-is** (no namespace rewriting). Tests run **serially** and clean up fixed namespaces after each demo.
- Changing a demo’s namespace or object names requires updating the matching test’s cleanup list in `test/e2e/demo_*_test.go`.
- Multus demos require Multus installed and the driver deployed with `DRA_DRIVER_MODE=MULTUS` / `kubeletPlugin.configurationMode=MULTUS`.
- VFIO and extended-resource demos run by default on the single-node virtual cluster (IOMMU + VFIO modules from deploy; dual PF via `eth0`/`eth1`). Skip with `E2E_SKIP_VFIO=1` / `E2E_SKIP_EXTENDED_RESOURCE=1`.
- Alignment demos are skipped unless explicitly enabled via `E2E_ENABLE_ALIGNMENT=1` (requires a `gpu.example.com` DRA publisher).
- The resource-policies demo skips unless a node named `dra-ctlplane-0.dra.lab` exists.

## Layout

| Path | Role |
|------|------|
| `framework/` | clients, YAML apply, waits, exec, skips, debug dump, cleanup |
| `smoke_test.go` | non-demo smoke tests |
| `demo_*_test.go` | one file per demo directory |
| `e2e_suite_test.go` | suite bootstrap and failure debug dumps |

All test files under `test/e2e/` (except the `!e2e` stub) use `//go:build e2e` so `make test` does not run them. The `framework` package has no build tag so editors/gopls can type-check it without extra build tags; unit-test coverage excludes `test/e2e/` (see `COVERAGE_EXCLUDE` in the Makefile).
