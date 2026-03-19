# DRA Attributes Downward API

## Overview

This enhancement provides containers with access to Dynamic Resource Allocation (DRA) device attributes and networking information through files mounted into the container filesystem. This allows applications to discover allocated SR-IOV VF attributes, networking configuration, and CNI results without querying the Kubernetes API.

Based on upstream KEP: [KEP-5304: DRA Attributes Downward API](https://github.com/kubernetes/enhancements/blob/97e2ebed48363deeec45be11d59efe072d9570d5/keps/sig-node/5304-dra-attributes-downward-api/README.md)

## File Structure

### Node-Level Storage
Files are stored on the node at:
```
/etc/dra/<pod_uid>/<claimName>.<requestName>.json
```

### Container-Level Access
Within containers, files are mounted to:
```
/etc/dra/<claimName>.<requestName>.json
```

Each file corresponds to a unique `(claimName, requestName)` tuple, supporting scenarios with multiple resource claims per pod.

## File Format

Each JSON file is a Kubernetes-style API object containing claim metadata and per-device information:

```json
{
  "apiVersion": "dra.k8s.io/v1alpha1",
  "kind": "DeviceMetadata",
  "metadata": {
    "name": "my-claim",
    "namespace": "default",
    "uid": "abc-123-def-456"
  },
  "requests": [
    {
      "name": "gpu-request",
      "devices": [
        {
          "name": "gpu-0",
          "driver": "nvidia.com",
          "pool": "node-1-gpus",
          "bestEffortData": {
            "attributes": {
              "model": "A100",
              "memory": "80Gi",
              "vendor": "nvidia"
            }
          },
          "driverProvidedData": {
            "conditions": [
              {
                "type": "Ready",
                "status": "True",
                "lastTransitionTime": "2024-01-15T10:00:00Z"
              }
            ],
            "data": {
              "pciBusID": "0000:00:1e.0"
            }
          }
        }
      ]
    },
    {
      "name": "network-request",
      "devices": [
        {
          "name": "vf-3",
          "driver": "cni.dra.networking.x-k8s.io",
          "pool": "node-1-sriov",
          "bestEffortData": {
            "attributes": {
              "vendor": "mellanox",
              "model": "ConnectX-6"
            }
          },
          "driverProvidedData": {
            "conditions": [
              {
                "type": "Ready",
                "status": "True",
                "lastTransitionTime": "2024-01-15T10:00:00Z"
              }
            ],
            "data": {
              "pciAddress": "0000:00:01.3",
              "vfIndex": 3,
              "mtu": 9000
            },
            "networkData": {
              "interfaceName": "net1",
              "addresses": ["10.10.1.2/24", "fd00::2/64"],
              "hwAddress": "5a:9f:d8:84:fb:51"
            }
          }
        }
      ]
    }
  ]
}
```
## Implementation

### High-Level Workflow

1. **CDI (Container Device Interface)**: Prepares the pod manifest with volume mounts, specifying that the DRA attribute files should be mounted into the container at `/etc/dra/`

2. **NRI (Node Resource Interface)**: Writes the JSON files to the node filesystem at `/etc/dra/<pod_uid>/` after CNI network attachment completes and networking information is available

3. **Container Runtime**: Mounts the files into the container at startup, ensuring the container has access to device attributes throughout its lifetime

### Lifecycle

- Files are created during pod sandbox creation after network attachment succeeds
- Files persist on the node for the pod's lifetime
- The container receives a consistent view of the allocated devices from initialization

## Future Considerations

When Kubernetes adds native support for DRA attributes in the Downward API, the base directory path may change while maintaining the same file structure and naming convention.
