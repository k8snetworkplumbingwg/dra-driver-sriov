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
helm install -n dra-sriov-driver --create-namespace dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

Install a specific stable version:
```bash
helm install -n dra-sriov-driver --create-namespace --version 1.0.0 dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

Install the latest from main branch (for testing):
```bash
helm install -n dra-sriov-driver --create-namespace --version 0.0.0-latest dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

Install a specific commit from main branch:
```bash
helm install -n dra-sriov-driver --create-namespace --version 0.0.0-a1b2c3d dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart
```

#### Deploy from project sources

```bash
# Clone project
git clone https://github.com/k8snetworkplumbingwg/dra-driver-sriov.git
cd dra-driver-sriov

# Install Driver
helm install -n dra-sriov-driver --create-namespace --wait dra-driver-sriov ./deployments/helm/dra-driver-sriov

# View deployed resources
kubectl -n dra-sriov-driver get pods
```

In the case that [Pod Security Admission](https://kubernetes.io/docs/concepts/security/pod-security-admission/) is enabled, the driver namespace will require a security level of 'privileged'
```bash
kubectl label ns dra-sriov-driver pod-security.kubernetes.io/enforce=privileged
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
helm uninstall -n dra-sriov-driver dra-driver-sriov
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
| `kubeletPlugin.containers.init.securityContext` | object | `{}` | Security context for init container |
| `kubeletPlugin.containers.init.resources` | object | `{}` | Resource requests/limits for init container |
| `kubeletPlugin.containers.plugin.securityContext` | object | `{"privileged":true}` | Security context for plugin container (requires privileged) |
| `kubeletPlugin.containers.plugin.resources` | object | `{}` | Resource requests/limits for plugin container |
| `kubeletPlugin.containers.plugin.healthcheckPort` | int | `-1` | Port for health check (disabled if negative) |

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
  -n dra-sriov-driver --create-namespace
```

### Installation with Custom Node Selection

Deploy the kubelet plugin only on nodes with SR-IOV hardware:

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-sriov-driver --create-namespace \
  --set kubeletPlugin.nodeSelector."feature\.node\.kubernetes\.io/network-sriov\.capable"="true"
```

### Installation with Increased Logging

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-sriov-driver --create-namespace \
  --set logging.level=5 \
  --set logging.format=json
```

### Installation with Resource Limits

```bash
helm install dra-driver-sriov oci://ghcr.io/k8snetworkplumbingwg/dra-driver-sriov-chart \
  -n dra-sriov-driver --create-namespace \
  --set kubeletPlugin.containers.plugin.resources.requests.cpu=100m \
  --set kubeletPlugin.containers.plugin.resources.requests.memory=128Mi \
  --set kubeletPlugin.containers.plugin.resources.limits.cpu=500m \
  --set kubeletPlugin.containers.plugin.resources.limits.memory=512Mi
```

## More Information

For more information about the DRA Driver for SR-IOV, visit:
- [Project Repository](https://github.com/k8snetworkplumbingwg/dra-driver-sriov)
- [Kubernetes DRA Documentation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)

