# DRA Driver for SR-IOV Helm Chart

DRA Driver for SR-IOV Helm Chart provides an easy way to install, configure and manage
the lifecycle of the Dynamic Resource Allocation (DRA) driver for SR-IOV devices.

## DRA Driver for SR-IOV

The DRA Driver for SR-IOV leverages [Kubernetes Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) 
to manage and allocate SR-IOV Virtual Functions (VFs) to Kubernetes pods.

DRA Driver for SR-IOV features:
- Discovers SR-IOV devices on cluster nodes
- Allocates SR-IOV VFs dynamically to pods using DRA
- Integrates with NRI (Node Resource Interface) for container configuration
- Supports device selection based on resource claims
- Provides fine-grained control over SR-IOV resource allocation

## QuickStart

### Prerequisites

- Kubernetes v1.34+ (with DRA feature enabled)
- Helm v3
- SR-IOV capable hardware on worker nodes
- NRI enabled on the container runtime

### Install Helm

Helm provides an install script to copy helm binary to your system:
```bash
curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3
chmod 500 get_helm.sh
./get_helm.sh
```

For additional information and methods for installing Helm, refer to the official [helm website](https://helm.sh/)

### Deploy DRA Driver for SR-IOV

#### Deploy from OCI repo

Install the latest stable release (recommended for production):
```bash
helm install -n dra-driver-sriov --create-namespace dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

Install a specific stable version:
```bash
helm install -n dra-driver-sriov --create-namespace --version 1.0.0 dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

Install the latest from main branch (for testing):
```bash
helm install -n dra-driver-sriov --create-namespace --version 0.0.0-latest dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

Install a specific commit from main branch:
```bash
helm install -n dra-driver-sriov --create-namespace --version 0.0.0-a1b2c3d dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

#### Deploy from project sources

```bash
# Clone project
git clone https://github.com/k8snetworkplumbingwg/dra-driver-sriov.git
cd dra-driver-sriov

# Install Driver
helm install -n dra-driver-sriov --create-namespace --wait dra-driver-sriov ./deployments/helm/dra-driver-sriov

# View deployed resources
kubectl -n dra-driver-sriov get pods
```

The chart creates no DeviceClass by default. Install administrator-managed
classes and protected NADs separately, or provide classes with `deviceClasses.items`.

For upgrades from a release that created the historical
`sriovnetwork.k8snetworkplumbingwg.io` DeviceClass, explicitly set
`deviceClasses.createDefault=true` to retain it, or migrate **every** workload
reference to approved classes before Helm removes it. The historical class has
no `VfConfig`; STANDALONE requires a configured replacement class with the
appropriate NAD settings, or explicit VFIO-without-CNI configuration.

1. Install the approved classes and protected NADs before creating replacement
   claims/templates. Use new template names for configuration-free requests;
   ResourceClaimTemplate specs are immutable, and replacing a template does not
   update ResourceClaims already owned by existing Pods.
2. Record the original replica counts and suspend HPA, GitOps, operator, or other
   automation that could restore replicas or recreate affected workloads. Scale
   affected Deployments and StatefulSets to zero before any manual Pod deletion
   or legacy-state cleanup; pausing a Deployment rollout alone is insufficient.
   Keep the old driver running until old Pods, generated ResourceClaims, and
   prepared state are removed; release unused direct claims separately. Include
   **all workloads with legacy prepared state**, even if their claims contain no
   SR-IOV override. Do not upgrade until cleanup finishes successfully.
3. While workloads remain scaled to zero, update their claim/template references
   and upgrade the driver with the chosen class-retention setting. Only after
   references are updated and the driver upgrade succeeds, restore the recorded
   replica counts and resume automation with its desired references and replica
   settings updated. Fresh Pods and claims must use the new policy. Retire old
   templates/classes only after no workloads reference them.

Downgrading to a driver that predates DeviceClass provenance requires cleaning
up **all prepared claims on every node before replacing the current driver**.
The checkpoint checksum includes `preparedClaimProvenance`; an older driver
discards that unknown field and fails checksum verification while metadata
remains. Suspend replica-restoring automation and scale affected workloads to
zero as above, then let the current driver complete normal unprepare. Retry
failed unprepare operations until cleanup succeeds and verify the affected host
artifacts. ResourceClaim deletion alone is not proof of cleanup.
On each node, verify both checkpoint maps are empty (adjust the plugin path if
configured differently):

```bash
sudo jq -e '
  ((.v1.preparedClaimsByPodUID // {}) | length) == 0 and
  ((.v1.preparedClaimProvenance // {}) | length) == 0
' /var/lib/kubelet/plugins/sriovnetwork.k8snetworkplumbingwg.io/checkpoint.json
```

Only after cleanup succeeds and the check prints `true` on every node may the
driver be downgraded. Restore workload manifests supported by the target driver
before restoring replicas and automation. If a downgrade was attempted early and
the old driver cannot load the checkpoint, restore the newer driver with its
previous configuration, finish cleanup, and retry the downgrade. Do not delete
or edit checkpoints or bypass checksum verification to force a downgrade.

Use the same cleanup-first procedure when switching development builds with
different checkpoint metadata. Earlier development builds that persisted extra
lifecycle metadata must finish cleanup before moving to this build.

In the case that [Pod Security Admission](https://kubernetes.io/docs/concepts/security/pod-security-admission/) is enabled, the driver namespace will require a security level of 'privileged'
```bash
kubectl label ns dra-driver-sriov pod-security.kubernetes.io/enforce=privileged
```

### Chart Versioning

The Helm chart follows this versioning scheme:

| Chart Version | Container Image Tag | Use Case |
|---------------|-------------------|----------|
| `1.0.0`, `1.1.0`, etc. | `v1.0.0`, `v1.1.0` | Stable releases (production) |
| `0.0.0-latest` | `latest` | Latest from main branch (testing) |
| `0.0.0-a1b2c3d` | `a1b2c3d` | Specific commit from main (reproducible testing) |

### Uninstall

```bash
helm uninstall -n dra-driver-sriov dra-driver-sriov
```

## Chart Parameters

In order to tailor the deployment of the DRA driver to your cluster needs,
the following chart parameters are available.

### Global Parameters

| Name | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `nameOverride` | string | `""` | Override the name of the chart |
| `fullnameOverride` | string | `""` | Override the full name of the chart |
| `namespaceOverride` | string | `""` | Override the namespace where resources are deployed |
| `selectorLabelsOverride` | object | `{}` | Override selector labels |
| `allowDefaultNamespace` | bool | `false` | Allow deployment in the default namespace |
| `imagePullSecrets` | list | `[]` | Image pull secrets for private registries |

### DeviceClass and admission parameters

| Name | Default | Description |
| ---- | ------- | ----------- |
| `deviceClasses.createDefault` | `false` | Explicitly create the historical class selecting all advertised SR-IOV devices. Restrict or remove it in multi-tenant clusters. |
| `deviceClasses.items` | `[]` | Administrator-managed entries with unique `name` and complete `spec`. The historical broad class name is reserved. Empty permits externally managed classes. |
| `admission.claimConfiguration.enabled` | `true` | Render claim/template validation policies and bindings. Hardened deployments disabling this require equivalent external policies. |
| `admission.claimConfiguration.mode` | `Deny` | `Deny` rejects claim configuration; `Audit` renders Audit/Warn for migration inventory. The binary always rejects claim overrides. |

For example, to install an approved VFIO class without CNI:

```yaml
deviceClasses:
  items:
  - name: sriov-vfio
    spec:
      selectors:
      - cel:
          expression: device.driver == 'sriovnetwork.k8snetworkplumbingwg.io'
      config:
      - opaque:
          driver: sriovnetwork.k8snetworkplumbingwg.io
          parameters:
            apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
            kind: VfConfig
            driver: vfio-pci
```

Narrow selectors and apply administrator-owned namespace ResourceQuotas for the
intended deployment. Check installed policy bindings, not only rendered output.

The installer needs permission to manage DeviceClasses and admission policies;
the node driver's service account receives no additional class-write or binding
permissions. Class-write RBAC does not restrict which namespaces may request a
class: configure namespace admission and per-class ResourceQuotas separately.
Hardened multi-tenant deployments must also verify Kubernetes 1.36 granular
claim-status authorization; the chart's 1.34 version floor alone is insufficient.

### Image Parameters

| Name | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `image.repository` | string | `ghcr.io/k8snetworkplumbingwg/dra-driver-sriov` | Container image repository |
| `image.pullPolicy` | string | `Always` | Image pull policy |
| `image.tag` | string | `""` | Image tag (defaults to chart appVersion) |

### Service Account Parameters

| Name | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `serviceAccount.create` | bool | `true` | Create a service account |
| `serviceAccount.annotations` | object | `{}` | Annotations for the service account |
| `serviceAccount.name` | string | `""` | Name of the service account (auto-generated if empty) |

### Kubelet Plugin Parameters

The kubelet plugin runs as a DaemonSet on all nodes where SR-IOV devices should be managed. This component handles both the controller logic and the node-level device management.

| Name | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `kubeletPlugin.priorityClassName` | string | `system-node-critical` | Priority class for the plugin |
| `kubeletPlugin.updateStrategy.type` | string | `RollingUpdate` | Update strategy for the DaemonSet |
| `kubeletPlugin.podAnnotations` | object | `{}` | Annotations for plugin pods |
| `kubeletPlugin.podSecurityContext` | object | `{}` | Security context for plugin pods |
| `kubeletPlugin.nodeSelector` | object | `{}` | Node selector for plugin placement |
| `kubeletPlugin.tolerations` | list | `[]` | Tolerations for plugin pods |
| `kubeletPlugin.affinity` | object | `{}` | Affinity rules for plugin |
| `kubeletPlugin.kubeletRegistrarDirectoryPath` | string | `/var/lib/kubelet/plugins_registry` | Path to kubelet plugin registration directory |
| `kubeletPlugin.kubeletPluginsDirectoryPath` | string | `/var/lib/kubelet/plugins` | Path to kubelet plugins directory |
| `kubeletPlugin.nriPluginName` | string | `dra-driver-sriov` | Name of the NRI plugin |
| `kubeletPlugin.nriPluginIndex` | int | `42` | Index of the NRI plugin (determines execution order) |
| `kubeletPlugin.defaultInterfacePrefix` | string | `vfnet` | Default prefix for network interface names |
| `kubeletPlugin.configurationMode` | string | `STANDALONE` | Driver networking mode. Supported values: `STANDALONE` (default, with NRI-based interface management) and `MULTUS` (delegates network attachment to Multus). |
| `kubeletPlugin.metrics.enabled` | bool | `false` | Enable the controller-runtime Prometheus metrics endpoint |
| `kubeletPlugin.metrics.port` | int | `8080` | Host-network port for the metrics endpoint when enabled |
| `kubeletPlugin.containers.init.securityContext` | object | `{}` | Security context for init container |
| `kubeletPlugin.containers.init.resources` | object | `{}` | Resource requests/limits for init container |
| `kubeletPlugin.containers.plugin.securityContext` | object | `{"privileged":true}` | Security context for plugin container (requires privileged) |
| `kubeletPlugin.containers.plugin.resources` | object | `{}` | Resource requests/limits for plugin container |
| `kubeletPlugin.containers.plugin.healthcheckPort` | int | `-1` | Port for health check (disabled if negative) |

Limitation for `MULTUS` mode: The runtime DRA device metadata update path is not active, so KEP-5304 DRA device metadata via CDI-mounted files is not supported in this mode.

### Logging Parameters

| Name | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `logging.level` | int | `3` | Log verbosity level (0-9, higher is more verbose) |
| `logging.format` | string | `text` | Log format (text or json) |
| `logging.alsologtostderr` | bool | `true` | Log to stderr in addition to log files |
| `logging.logFile` | string | `""` | Path to log file (empty means stderr only) |

## Example Configurations

### Minimal Installation

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-driver-sriov --create-namespace
```

### Installation with Custom Node Selection

Deploy the kubelet plugin only on nodes with SR-IOV hardware:

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-driver-sriov --create-namespace \
  --set kubeletPlugin.nodeSelector."feature\.node\.kubernetes\.io/network-sriov\.capable"="true"
```

### Installation with Increased Logging

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-driver-sriov --create-namespace \
  --set logging.level=5 \
  --set logging.format=json
```

### Installation with Resource Limits

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-driver-sriov --create-namespace \
  --set kubeletPlugin.containers.plugin.resources.requests.cpu=100m \
  --set kubeletPlugin.containers.plugin.resources.requests.memory=128Mi \
  --set kubeletPlugin.containers.plugin.resources.limits.cpu=500m \
  --set kubeletPlugin.containers.plugin.resources.limits.memory=512Mi
```

## More Information

For more information about the DRA Driver for SR-IOV, visit:
- [Project Repository](https://github.com/k8snetworkplumbingwg/dra-driver-sriov)
- [Kubernetes DRA Documentation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)

