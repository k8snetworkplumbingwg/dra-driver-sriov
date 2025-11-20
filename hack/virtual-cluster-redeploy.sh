#!/usr/bin/env bash
set -xeo pipefail

HOME="/root"
here="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
root="$(readlink --canonicalize "$here/..")"
domain_name=lab

NAMESPACE="dra-driver-sriov"
controller_ip=`kubectl get node -o wide | grep ctlp | awk '{print $6}'`
SRIOV_DRIVER_IMAGE="$controller_ip:5000/dra-driver-sriov"

echo "## build driver image"
CONTAINER_TOOL=podman IMAGE_NAME=${SRIOV_DRIVER_IMAGE} make -C deployments/container/

podman push --tls-verify=false "${SRIOV_DRIVER_IMAGE}"
podman rmi -fi ${SRIOV_DRIVER_IMAGE}

# Deploy the dra driver via helm
set +e
make helm
set -e
${root}/bin/helm upgrade -i dra-driver-sriov deployments/helm/dra-driver-sriov/ --namespace dra-driver-sriov --create-namespace --set image.repository=${SRIOV_DRIVER_IMAGE}

kubectl -n ${NAMESPACE} delete po --all
