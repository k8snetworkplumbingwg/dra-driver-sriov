#!/usr/bin/env bash

cluster_version=${CLUSTER_VERSION:-1.36.0}
cluster_name=${CLUSTER_NAME:-dra}
domain_name=$cluster_name.lab
network_name=${NETWORK_NAME:-dra}

api_ip=${API_IP:-192.168.120.250}
virtual_router_id=${VIRTUAL_ROUTER_ID:-200}

export NAMESPACE="dra-driver-sriov"
export KUBECONFIG=/root/.kcli/clusters/$cluster_name/auth/kubeconfig

export DRA_DRIVER_MODE=${DRA_DRIVER_MODE:-STANDALONE}

here="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
root="$(readlink --canonicalize "$here/..")"
