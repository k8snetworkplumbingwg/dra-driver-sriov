# DRA Driver for SR-IOV Virtual Functions

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](http://www.apache.org/licenses/LICENSE-2.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/k8snetworkplumbingwg/dra-driver-sriov)](https://goreportcard.com/report/github.com/k8snetworkplumbingwg/dra-driver-sriov)
[![Coverage Status](https://coveralls.io/repos/github/k8snetworkplumbingwg/dra-driver-sriov/badge.svg)](https://coveralls.io/github/k8snetworkplumbingwg/dra-driver-sriov)

A Kubernetes Dynamic Resource Allocation (DRA) driver that enables exposure and management of SR-IOV virtual functions as cluster resources.

## Overview

This project implements a DRA driver that allows Kubernetes workloads to request and use SR-IOV virtual functions through the native Kubernetes resource allocation system. The driver integrates with the kubelet plugin system to manage SR-IOV VF lifecycle, including discovery, allocation, and cleanup.

The driver features an advanced resource filtering system that enables administrators to define fine-grained policies for how Virtual Functions are exposed and allocated based on hardware characteristics such as vendor ID, device ID, Physical Function names, PCI addresses, NUMA topology, and more.

## Features

- **Dynamic Resource Allocation**: Leverages Kubernetes DRA framework for SR-IOV VF management
- **Advanced Resource Filtering**: Fine-grained filtering of Virtual Functions based on hardware attributes
- **Custom Resource Definitions**: SriovResourceFilter CRD for configuring device filtering policies
- **Controller-based Management**: Kubernetes controller pattern for resource filter lifecycle management
- **Multiple Resource Types**: Support for exposing different VF pools as distinct resource types
- **Node-targeted Filtering**: Per-node resource filtering with node selector support
- **CDI Integration**: Uses Container Device Interface for device injection into containers
- **NRI Integration**: Node Resource Interface support for advanced container runtime interaction
- **Kubernetes Native**: Integrates seamlessly with standard Kubernetes resource request/limit model
- **CNI Plugin Support**: Integrates with SR-IOV CNI for network configuration
- **VFIO Driver Support**: Support for both kernel and VFIO-PCI driver binding modes
- **Vhost-user Integration**: Optional mounting of vhost-user sockets for DPDK and userspace networking
- **Health Monitoring**: Built-in health check endpoints for monitoring driver status
- **Helm Deployment**: Easy deployment through Helm charts

## Requirements

- Kubernetes 1.34.0 or later (with DRA support enabled)
- SR-IOV capable network hardware  
- Container runtime with CDI support
- Container runtime with NRI plugins support


## Building

To build the container image, use the following command:

```bash
CONTAINER_TOOL=podman IMAGE_NAME=localhost/dra-driver-sriov VERSION=latest make -f deployments/container/Makefile
```

You can customize the build by setting different environment variables:
- `CONTAINER_TOOL`: Container tool to use (docker, podman)
- `IMAGE_NAME`: Container image name and registry
- `VERSION`: Image tag version

### Building Binaries

To build just the binaries without containerizing:

```bash
make cmds
```

Or to build for specific platforms:

```bash
GOOS=linux GOARCH=amd64 make cmds
```

## Deployment

Deploy the DRA driver using Helm:

```bash
helm upgrade -i sriov-dra --create-namespace -n dra-driver-sriov ./deployments/helm/dra-driver-sriov/
```

### Configuration Options

The Helm chart supports various configuration options through `values.yaml`:

- **Image Configuration**: Customize image repository, tag, and pull policy
- **Resource Limits**: Set resource requests and limits for driver components  
- **Node Selection**: Configure node selectors and tolerations
- **Namespace Configuration**: Configure the namespace where SriovResourceFilter resources are watched
- **Default Interface Prefix**: Set the default interface prefix for virtual functions
- **CDI Root**: Configure the directory for CDI file generation
- **Logging**: Adjust log verbosity and format
- **Security**: Configure security contexts and service accounts
- **Health Check**: Configure health check endpoints

Example custom deployment:

```bash
helm upgrade -i sriov-dra \
  --create-namespace -n dra-sriov-driver \
  --set image.tag=v0.1.0 \
  --set logging.level=5 \
  --set driver.namespace=dra-sriov-driver \
  --set driver.defaultInterfacePrefix=net \
  ./deployments/helm/dra-driver-sriov/
```

## Usage

Once deployed, workloads can request SR-IOV virtual functions using ResourceClaimTemplates:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: sriov-vf
spec:
  spec:
    devices:
      requests:
      - name: vf
        exactly:
          deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
```

Then reference the claim in your Pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sriov-workload
spec:
  containers:
  - name: app
    image: your-app:latest
    resources:
      claims:
      - name: vf
  resourceClaims:
  - name: vf
    resourceClaimTemplateName: sriov-vf
```

## Resource Filtering System

The DRA driver includes an advanced resource filtering system that allows administrators to define fine-grained policies for how SR-IOV Virtual Functions are exposed and allocated. This system uses Custom Resource Definitions (CRDs) and a Kubernetes controller to manage device filtering based on hardware characteristics.

### SriovResourceFilter CRD

The `SriovResourceFilter` custom resource allows you to define filtering policies for SR-IOV devices:

```yaml
apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
kind: SriovResourceFilter
metadata:
  name: example-filter
  namespace: dra-sriov-driver
spec:
  nodeSelector:
    kubernetes.io/hostname: worker-node-1
  configs:
  - resourceName: "eth0_resource"  
    resourceFilters:
    - vendors: ["8086"]           # Intel devices only
      pfNames: ["eth0"]           # Physical Function name
      numaNodes: ["0"]            # NUMA node 0 only
  - resourceName: "eth1_resource"
    resourceFilters:  
    - vendors: ["8086"]
      pfNames: ["eth1"]
      drivers: ["vfio-pci"]       # Only VFIO-bound devices
```

### Filtering Criteria

The resource filtering system supports multiple filtering criteria that can be combined:

- **vendors**: Filter by PCI vendor ID (e.g., "8086" for Intel)
- **devices**: Filter by PCI device ID 
- **pciAddresses**: Filter by specific PCI addresses
- **pfNames**: Filter by Physical Function name (e.g., "eth0", "eth1")
- **rootDevices**: Filter by parent PCI address
- **numaNodes**: Filter by NUMA node topology

### Node Selection

Use `nodeSelector` to target specific nodes:

```yaml
spec:
  nodeSelector:
    kubernetes.io/hostname: specific-node
    # or
    node-type: sriov-enabled
    # Empty nodeSelector matches all nodes
```

### Multiple Resource Types

Define multiple resource configurations to create different pools of Virtual Functions:

```yaml
spec:
  configs:
  - resourceName: "high-performance"
    resourceFilters:
    - vendors: ["8086"]
      pfNames: ["eth0"]
      numaNodes: ["0"]
  - resourceName: "standard-networking"  
    resourceFilters:
    - vendors: ["8086"]  
      pfNames: ["eth1"]
```

### Using Filtered Resources

Once a `SriovResourceFilter` is applied, pods can request specific resource types using CEL expressions:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: filtered-vf
spec:
  spec:
    devices:
      requests:
      - name: vf
        exactly:
          deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
          selectors:
          - cel:
              expression: device.attributes["sriovnetwork.k8snetworkplumbingwg.io"].resourceName == "eth0_resource"
```

## VfConfig Parameters

The `VfConfig` resource defines how Virtual Functions are configured and exposed to containers. All VfConfig parameters are optional with sensible defaults:

### Core Parameters

- **`driver`**: Driver binding mode for the Virtual Function
  - `""` (default): Use kernel networking driver
  - `"vfio-pci"`: Bind to VFIO-PCI driver for userspace access (DPDK, etc.)

- **`ifName`**: Network interface name inside the container
  - Default: Auto-generated (typically `net1`, `net2`, etc.)
  - Only relevant for kernel driver mode

- **`netAttachDefName`**: Reference to NetworkAttachmentDefinition resource
  - Defines CNI configuration for the interface
  - Required for network connectivity

- **`netAttachDefNamespace`**: Namespace of the NetworkAttachmentDefinition
  - Default: Same namespace as the pod
  - Optional parameter for cross-namespace references

### Advanced Parameters

- **`addVhostMount`**: Mount vhost-user sockets into the container
  - `false` (default): No vhost-user socket mounting
  - `true`: Mount vhost-user sockets for accelerated userspace networking
  - Typically used with DPDK applications requiring vhost-user interfaces
  - Creates socket paths accessible by userspace networking frameworks

### Usage Examples

**Basic Kernel Networking:**
```yaml
parameters:
  apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
  kind: VfConfig
  ifName: net1
  netAttachDefName: sriov-network
```

**VFIO for DPDK Applications:**
```yaml
parameters:
  apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
  kind: VfConfig
  driver: vfio-pci
  addVhostMount: true
  netAttachDefName: sriov-management
```

### Example Workloads

The `demo/` directory contains comprehensive example scenarios demonstrating different usage patterns:

#### Single VF Claim (`demo/single-vf-claim/`)
Complete example showing a pod requesting a single SR-IOV virtual function:
- Basic SR-IOV Virtual Function allocation using DRA
- Standard kernel-mode networking with SR-IOV acceleration  
- Simple container deployment with high-performance networking
- VfConfig parameters for kernel networking:
  - `ifName: net1`: Network interface name in the container
  - `netAttachDefName: vf-test1`: References the NetworkAttachmentDefinition
  - `driver`: Driver binding mode (default: kernel driver)
  - `addVhostMount`: Mount vhost-user sockets (default: false)

#### Multiple VF Claim (`demo/multiple-vf-claim/`)
Demonstrates requesting multiple Virtual Functions in a single resource claim:
- Allocates multiple VFs (configurable count) for high-availability scenarios
- Load balancing and bandwidth aggregation use cases
- Multi-interface networking setup
- VfConfig applies to all allocated VFs in the claim
- Automatic interface naming (typically net1, net2, etc.)

#### Resource Filtering (`demo/resource-filtering/`)  
Shows how to use SriovResourceFilter for advanced device management:
- Filter VFs based on vendor ID, Physical Function names, and hardware attributes
- Multiple resource configurations for different network interfaces
- Node-targeted filtering with selector support

#### VFIO Driver Configuration (`demo/vfio-driver/`)
Illustrates VFIO-PCI driver configuration for userspace applications:
- Configure Virtual Functions with VFIO-PCI driver for DPDK applications
- Device passthrough for high-performance userspace networking
- Vhost-user socket mounting for container networking acceleration
- VfConfig parameters for userspace networking:
  - `driver: vfio-pci`: Binds VF to VFIO-PCI driver instead of kernel driver
  - `addVhostMount: true`: Mounts vhost-user sockets into the container
  - `ifName`: Interface name (optional for VFIO mode)
  - `netAttachDefName`: Network attachment definition for management interface

## Project Structure

```
├── cmd/
│   └── dra-driver-sriov/          # Main driver executable
├── pkg/
│   ├── driver/                    # Core driver implementation
│   ├── controller/                # Kubernetes controller for resource filtering
│   ├── devicestate/               # Device state management and discovery
│   ├── api/                       # API definitions
│   │   ├── sriovdra/v1alpha1/     # SriovResourceFilter CRD definitions
│   │   └── virtualfunction/v1alpha1/ # Virtual Function API types
│   ├── cdi/                       # CDI integration
│   ├── cni/                       # CNI plugin integration
│   ├── nri/                       # NRI (Node Resource Interface) integration
│   ├── podmanager/                # Pod lifecycle management
│   ├── host/                      # Host system interaction
│   ├── types/                     # Type definitions and configuration
│   ├── consts/                    # Constants and driver configuration
│   └── flags/                     # Command-line flag handling
├── deployments/
│   ├── container/                 # Container build configuration
│   └── helm/                      # Helm chart
├── demo/                          # Example workload configurations
│   ├── single-vf-claim/           # Single VF allocation example
│   ├── multiple-vf-claim/         # Multiple VF allocation example
│   ├── resource-filtering/        # Resource filtering configuration example
│   └── vfio-driver/              # VFIO-PCI driver configuration example
├── hack/                          # Build and development scripts
├── test/                          # Test suites
└── vendor/                        # Go module dependencies
```

### Key Components

- **Driver**: Main gRPC service implementing DRA kubelet plugin interface
- **Resource Filter Controller**: Kubernetes controller managing SriovResourceFilter lifecycle and device filtering
- **Device State Manager**: Tracks available and allocated SR-IOV virtual functions with filtering support
- **SriovResourceFilter CRD**: Custom resource for defining device filtering policies  
- **CDI Generator**: Creates Container Device Interface specifications for VFs
- **NRI Plugin**: Node Resource Interface integration for container runtime interaction
- **Pod Manager**: Manages pod lifecycle and resource allocation
- **CNI Runtime**: Integrates with CNI plugins for network configuration
- **Host Interface**: System-level operations for device discovery and driver binding
- **Health Check**: Monitors driver health and readiness

## Development

### Prerequisites

- Go 1.24.6+
- Make
- Container tool (Docker/Podman)
- Kubernetes cluster with DRA enabled

### Building from Source

```bash
# Clone the repository
git clone https://github.com/k8snetworkplumbingwg/dra-driver-sriov.git
cd dra-driver-sriov

# Build binaries
make build

# Run tests
make test

# Build container image
make -f deployments/container/Makefile
```

### Testing

The project includes unit tests and end-to-end tests:

```bash
# Run unit tests
make test

# Run with coverage
make coverage

# Run linting and format checks
make check
```

## Contributing

We welcome contributions to the DRA Driver for SR-IOV Virtual Functions project!

### How to Contribute

1. **Fork the repository** on GitHub
2. **Create a feature branch** from `main`
3. **Make your changes** following the coding standards
4. **Add tests** for new functionality
5. **Ensure all tests pass** with `make test check`
6. **Submit a pull request** with a clear description

### Development Guidelines

- Follow Go conventions and use `gofmt` for formatting
- Write unit tests for new code
- Update documentation for user-facing changes
- Use semantic commit messages
- Ensure backward compatibility when possible

### Code Style

- Run `make fmt` to format code
- Run `make check` to verify linting and style
- Follow Kubernetes coding conventions
- Add appropriate logging with structured fields

### Reporting Issues

Please use GitHub Issues to report bugs or request features:

- Use clear, descriptive titles
- Provide detailed reproduction steps for bugs
- Include relevant logs and configuration
- Specify Kubernetes and driver versions

### License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

This project builds upon:
- [Kubernetes Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
- [Container Device Interface](https://github.com/cncf-tags/container-device-interface)
- [SR-IOV CNI](https://github.com/k8snetworkplumbingwg/sriov-cni)
