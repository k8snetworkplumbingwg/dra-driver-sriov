#!/usr/bin/env bash

cluster_version=${CLUSTER_VERSION:-1.34.2}
cluster_name=${CLUSTER_NAME:-dra}
domain_name=$cluster_name.lab
network_name=${NETWORK_NAME:-dra}

api_ip=${API_IP:-192.168.120.250}
virtual_router_id=${VIRTUAL_ROUTER_ID:-200}

here="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
root="$(readlink --canonicalize "$here/..")"
