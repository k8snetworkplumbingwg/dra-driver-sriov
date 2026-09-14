#!/usr/bin/env bash
set -xeo pipefail

source hack/common.sh

NUM_OF_WORKERS=${NUM_OF_WORKERS:-2}
export OPERATOR_EXEC=kubectl

cleanup_only=false
for arg in "$@"; do
  case "$arg" in
    --cleanup)
      cleanup_only=true
      ;;
    --single-node)
      NUM_OF_WORKERS=0
      ;;
    -h|--help)
      cat <<EOF
Usage: $0 [--cleanup] [--single-node]

Deploy a kcli-based virtual Kubernetes cluster with SR-IOV VFs and the DRA driver.

  (default)      Tear down any existing cluster, then deploy a new multi-node cluster
                 (1 control plane + NUM_OF_WORKERS workers, default 2).
  --single-node  Deploy a single-node cluster (control plane only, acts as worker).
  --cleanup      Tear down the cluster and exit.

Environment:
  NUM_OF_WORKERS     Worker count (default: 2; use 0 or --single-node for single-node).
  DRA_DRIVER_MODE    STANDALONE (default) or MULTUS.
  CLUSTER_NAME       Cluster name (default: dra).
  CLUSTER_VERSION    Kubernetes version (default: 1.36.1).
EOF
      exit 0
      ;;
    *)
      echo "Unknown argument: $arg" >&2
      echo "Usage: $0 [--cleanup] [--single-node]" >&2
      exit 1
      ;;
  esac
done

if [[ "$NUM_OF_WORKERS" -eq 0 ]]; then
  SINGLE_NODE=true
  total_number_of_nodes=1
  registry_storage=20Gi
else
  SINGLE_NODE=false
  total_number_of_nodes=$((1 + NUM_OF_WORKERS))
  registry_storage=60Gi
fi

sriov_network_name="${cluster_name}-sriov"

check_requirements() {
  local -a cmds=(kcli virsh podman make go)
  for cmd in "${cmds[@]}"; do
    if ! command -v "$cmd" &> /dev/null; then
      echo "$cmd is not available"
      exit 1
    fi
  done
}

# kcli delete wrapper; ignore failure only if output confirms the resource is absent.
kcli_delete() {
  local out rc=0
  out=$(kcli delete "$@" -y 2>&1) || rc=$?
  [[ -n "$out" ]] && printf '%s\n' "$out"
  if [[ $rc -ne 0 ]] && ! grep -qi 'not found' <<<"$out"; then
    return "$rc"
  fi
  return 0
}

cleanup() {
  echo "## cleaning up cluster $cluster_name"
  kcli_delete cluster "$cluster_name"
  kcli_delete network "$cluster_name"
  kcli_delete network "${sriov_network_name}"
  kcli_delete network "${network_name}"
  sudo rm -f "/etc/containers/registries.conf.d/003-${cluster_name}.conf"
}

create_networks() {
  kcli create network -c 192.168.120.0/24 "${network_name}"
  kcli create network -c "192.168.${virtual_router_id}.0/24" --nodhcp -i "${sriov_network_name}"
}

write_cluster_plan() {
  local sriov_vmrule
  sriov_vmrule=$(cat <<EOF
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
          numa: 0
        - name: ${sriov_network_name}
          type: igb
          vfio: true
          noconf: true
          numa: 1
      numcpus: 6
      numa:
        - id: 0
          vcpus: 0,2,4
          memory: 2048
        - id: 1
          vcpus: 1,3,5
          memory: 2048
EOF
)

  cat <<EOF > "./${cluster_name}-plan.yaml"
version: $cluster_version
ctlplane_memory: 4096
worker_memory: 4096
pool: default
disk_size: 30
network: ${network_name}
api_ip: $api_ip
virtual_router_id: $virtual_router_id
domain: $domain_name
ctlplanes: 1
workers: $NUM_OF_WORKERS
ingress: false
machine: q35
engine: crio
sdn: flannel
autolabeller: false
vmrules:
$(if $SINGLE_NODE; then
  printf '  - %s-ctlplane-.*:\n%s\n' "$cluster_name" "$sriov_vmrule"
else
  cat <<VMRULES
  - $cluster_name-ctlplane-.*:
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
  - $cluster_name-worker-.*:
$sriov_vmrule
VMRULES
fi)
EOF
}

wait_for_cluster_ready() {
  local attempts=0
  local max_attempts=72
  local ready=false
  local sleep_time=10

  until $ready || [[ $attempts -eq $max_attempts ]]; do
    if [[ $(kubectl get nodes --no-headers | grep -c " Ready ") -eq $total_number_of_nodes ]]; then
      ready=true
    fi
    if $ready; then
      echo "Cluster is ready"
    else
      echo "Cluster is not ready yet"
      sleep "$sleep_time"
    fi
    attempts=$((attempts + 1))
  done

  if ! $ready; then
    echo "Timed out waiting for cluster to be ready"
    kubectl get nodes
    exit 1
  fi
}

label_nodes() {
  if $SINGLE_NODE; then
    echo "## untaint control plane to allow scheduling workloads"
    kubectl taint nodes --all node-role.kubernetes.io/control-plane- || true

    echo "## label control plane as sriov capable and as worker"
    kubectl label node "${cluster_name}-ctlplane-0.${domain_name}" \
      feature.node.kubernetes.io/network-sriov.capable=true --overwrite
    kubectl label node "${cluster_name}-ctlplane-0.${domain_name}" \
      node-role.kubernetes.io/worker= --overwrite
    return
  fi

  echo "## label cluster workers as sriov capable"
  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    kubectl label node "${cluster_name}-worker-${num}.${domain_name}" \
      feature.node.kubernetes.io/network-sriov.capable=true --overwrite
  done

  echo "## label cluster workers as worker"
  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    kubectl label node "${cluster_name}-worker-${num}.${domain_name}" \
      node-role.kubernetes.io/worker= --overwrite
  done
}

get_controller_ip() {
  controller_ip=$(kubectl get node "${cluster_name}-ctlplane-0.${domain_name}" \
    -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
  if [[ -z "$controller_ip" ]]; then
    echo "## ERROR: Failed to get controller IP"
    kubectl get nodes -o wide
    exit 1
  fi
}

configure_host_registry() {
  insecure_registry="[[registry]]
location = \"$controller_ip:5000\"
insecure = true

[aliases]
\"golang\" = \"docker.io/library/golang\"
"

  sudo bash -c "cat << EOF > /etc/containers/registries.conf.d/003-${cluster_name}.conf
$insecure_registry
EOF"
}

update_host() {
  local node_name=$1
  kcli ssh "$node_name" << EOF
sudo su
echo '$insecure_registry' > /etc/containers/registries.conf.d/003-internal.conf
systemctl restart crio

echo '[connection]
id=multi
type=ethernet
[ethernet]
[match]
driver=igbvf;
[ipv4]
method=disabled
[ipv6]
addr-gen-mode=default
method=disabled
[proxy]' > /etc/NetworkManager/system-connections/multi.nmconnection

chmod 600 /etc/NetworkManager/system-connections/multi.nmconnection

echo '[Unit]
Description=disable checksum offload to avoid vf bug
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -c "ethtool --offload  eth1  rx off  tx off && ethtool -K eth1 gso off"
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=default.target' > /etc/systemd/system/disable-offload.service

systemctl daemon-reload
systemctl enable --now disable-offload

echo '[Unit]
Description=load br_netfilter
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -c "modprobe br_netfilter"
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=default.target' > /etc/systemd/system/load-br-netfilter.service

systemctl daemon-reload
systemctl enable --now load-br-netfilter

cat > /usr/local/bin/create-sriov-vfs.sh << 'VFSCRIPT'
#!/usr/bin/bash
set -euo pipefail
created=0
for driver_link in /sys/bus/pci/devices/*/driver; do
  [[ -e "\$driver_link" ]] || continue
  readlink -f "\$driver_link" | grep -q '/igb\$' || continue
  pf="\$(dirname "\$driver_link")"
  addr="\$(basename "\$pf")"
  [[ -w "\$pf/sriov_numvfs" ]] || continue
  [[ -r "\$pf/sriov_totalvfs" ]] || continue
  totalvfs="\$(cat "\$pf/sriov_totalvfs")"
  [[ "\$totalvfs" -ge 5 ]] || continue
  echo 0 > "\$pf/sriov_numvfs" || true
  echo 5 > "\$pf/sriov_numvfs"
  echo "Created VFs on \$addr"
  created=\$((created + 1))
done
if [[ "\$created" -eq 0 ]]; then
  echo "error: no igb PF with writable sriov_numvfs and sriov_totalvfs >= 5" >&2
  exit 1
fi
VFSCRIPT
chmod +x /usr/local/bin/create-sriov-vfs.sh

echo '[Unit]
Description=create sriov vfs
Before=network-pre.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/create-sriov-vfs.sh
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=network-pre.target' > /etc/systemd/system/create-sriov-vfs.service

systemctl daemon-reload
systemctl enable --now create-sriov-vfs

systemctl restart NetworkManager

grubby --update-kernel=DEFAULT --args=pci=realloc
grubby --update-kernel=DEFAULT --args=iommu=pt
grubby --update-kernel=DEFAULT --args=intel_iommu=on

echo '[Unit]
Description=load VFIO modules for DRA VFIO demos
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -c "modprobe vfio && modprobe vfio_iommu_type1 && modprobe vfio-pci"
RemainAfterExit=yes
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=multi-user.target' > /etc/systemd/system/load-vfio.service

systemctl daemon-reload
systemctl enable --now load-vfio

EOF
}

configure_hosts_and_reboot() {
  update_host "${cluster_name}-ctlplane-0"

  # Single-node reboots the control plane VM
  if $SINGLE_NODE; then
    kcli restart vm "${cluster_name}-ctlplane-0"
    return
  fi

  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    update_host "${cluster_name}-worker-${num}"
    kcli restart vm "${cluster_name}-worker-${num}"
  done
}

collect_node_diagnostics() {
  kubectl get nodes -o wide || true
  kubectl get events -A --sort-by=.lastTimestamp || true
  kubectl describe node "${cluster_name}-ctlplane-0.${domain_name}" || true
  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    kubectl describe node "${cluster_name}-worker-${num}.${domain_name}" || true
  done
}

wait_for_nodes_after_reboot() {
  # Single-node reboots the control plane VM, so the API server is down until
  # the VM finishes booting. Multi-node only reboots workers; the API stays up.
  if $SINGLE_NODE; then
    sleep 60
  fi

  local attempts=0
  local max_attempts=40
  local ready=false
  local sleep_time=15

  until $ready || [[ $attempts -eq $max_attempts ]]; do
    echo "waiting for API server to be reachable after reboot (attempt $attempts)"
    if kubectl get nodes &>/dev/null; then
      echo "API server is reachable, waiting for node ready..."
      if kubectl wait --for=condition=ready node --all --timeout=60s 2>/dev/null; then
        echo "node(s) ready"
        ready=true
      fi
    else
      sleep "$sleep_time"
    fi
    attempts=$((attempts + 1))
  done

  if ! $ready; then
    echo "## node readiness wait failed; collecting diagnostics"
    collect_node_diagnostics
    exit 1
  fi
}

fix_cni_bridge() {
  kcli ssh "${cluster_name}-ctlplane-0" << EOF
sudo su
if [ \$(ip a | grep 10.85.0 | wc -l) -eq 0 ]; then ip link del cni0 2>/dev/null || true; fi
EOF
}

recover_pod_networking() {
  local timeout=400

  kubectl -n kube-system delete po -l k8s-app=kube-dns --ignore-not-found=true

  echo "## wait for coredns"
  kubectl -n kube-system wait --for=condition=available deploy/coredns --timeout=${timeout}s
  echo "## wait for multus"
  # Force a new DaemonSet revision so rollout status waits for fresh Multus pods
  # instead of returning immediately on the previous completed revision.
  kubectl -n "${MULTUS_NAMESPACE}" rollout restart daemonset/kube-multus-ds
  kubectl -n "${MULTUS_NAMESPACE}" rollout status daemonset/kube-multus-ds --timeout=${timeout}s
}

deploy_cert_manager() {
  echo "## deploy cert manager"
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.12.0/cert-manager.yaml

  echo "## wait for cert manager to be ready"
  local attempts=0
  local max_attempts=72
  local ready=false
  local sleep_time=5

  until $ready || [[ $attempts -eq $max_attempts ]]; do
    echo "waiting for cert manager webhook to be ready"
    if [[ $(kubectl -n cert-manager get po | grep webhook | grep "1/1" | wc -l) -eq 1 ]]; then
      echo "cert manager webhook is ready"
      ready=true
    else
      echo "cert manager webhook is not ready yet"
      sleep "$sleep_time"
    fi
    attempts=$((attempts + 1))
  done

  if ! $ready; then
    echo "Timed out waiting for cert manager webhook to be ready"
    exit 1
  fi
}

deploy_internal_registry() {
  kubectl create namespace container-registry --dry-run=client -o yaml | kubectl apply -f -

  echo "## deploy internal registry"
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: registry-pv
spec:
  capacity:
    storage: ${registry_storage}
  volumeMode: Filesystem
  accessModes:
  - ReadWriteOnce
  persistentVolumeReclaimPolicy: Delete
  storageClassName: registry-local-storage
  local:
    path: /mnt/
  nodeAffinity:
    required:
      nodeSelectorTerms:
      - matchExpressions:
        - key: kubernetes.io/hostname
          operator: In
          values:
          - ${cluster_name}-ctlplane-0.${domain_name}
EOF

  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: registry-pv-claim
  namespace: container-registry
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: ${registry_storage}
  storageClassName: registry-local-storage
EOF

  cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: container-registry
spec:
  replicas: 1
  selector:
    matchLabels:
      app: registry
  template:
    metadata:
      labels:
        app: registry
    spec:
      hostNetwork: true
      tolerations:
        - effect: NoSchedule
          key: node-role.kubernetes.io/control-plane
      containers:
      - image: quay.io/libpod/registry:2.8.2
        imagePullPolicy: Always
        name: registry
        volumeMounts:
        - name: data
          mountPath: /var/lib/registry
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: registry-pv-claim
      terminationGracePeriodSeconds: 10
EOF

  echo "## wait for registry to be ready"
  kubectl -n container-registry wait --for=condition=available deploy/registry --timeout=120s
}

build_and_push_driver_image() {
  export SRIOV_DRIVER_IMAGE="$controller_ip:5000/dra-driver-sriov"

  CONTAINER_TOOL=podman IMAGE_NAME="${SRIOV_DRIVER_IMAGE}" make -C deployments/container/
  podman push --tls-verify=false "${SRIOV_DRIVER_IMAGE}"
  podman rmi -fi "${SRIOV_DRIVER_IMAGE}"
}

deploy_dra_driver() {
  set +e
  make helm
  set -e
  "${root}/bin/helm" upgrade -i dra-driver-sriov deployments/helm/dra-driver-sriov/ \
    --namespace dra-driver-sriov --create-namespace \
    --set "kubeletPlugin.configurationMode=${DRA_DRIVER_MODE}" \
    --set "image.repository=${SRIOV_DRIVER_IMAGE}"
}

wait_for_dra_driver_daemonset() {
  echo "## Waiting for daemonset to be ready..."
  local attempts=0
  local max_attempts=60

  while [[ $attempts -lt $max_attempts ]]; do
    local desired ready
    desired=$(kubectl -n dra-driver-sriov get ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin \
      -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo "0")
    ready=$(kubectl -n dra-driver-sriov get ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin \
      -o jsonpath='{.status.numberReady}' 2>/dev/null || echo "0")

    if [[ -n "$desired" && "$desired" != "0" && "$desired" == "$ready" ]]; then
      echo "## Daemonset is ready ($ready/$desired)"
      return 0
    fi
    echo "## Waiting for daemonset to be ready ($ready/$desired)..."
    sleep 5
    attempts=$((attempts + 1))
  done

  echo "## ERROR: Timed out waiting for daemonset to be ready"
  kubectl -n dra-driver-sriov get ds -o wide || true
  kubectl -n dra-driver-sriov get pods || true
  exit 1
}

verify_vfs_on_host() {
  local host_name=$1
  echo "## verify VFs were created after reboot on ${host_name}"
  kcli ssh "$host_name" << 'VERIFY_EOF'
echo "=== PCI ethernet devices ==="
lspci | grep -i ethernet 2>/dev/null || ls /sys/bus/pci/drivers/igb/ 2>/dev/null
echo "=== igb driver bindings ==="
ls -la /sys/bus/pci/drivers/igb/ 2>/dev/null | grep -E "^l" || echo "no igb bindings"
echo "=== sriov_totalvfs for all PCI devices ==="
for dev in /sys/bus/pci/devices/*/sriov_totalvfs; do
  if [ -f "$dev" ]; then
    addr=$(basename $(dirname $dev))
    total=$(cat $dev)
    numvfs=$(cat $(dirname $dev)/sriov_numvfs 2>/dev/null || echo "N/A")
    driver=$(basename $(readlink -f $(dirname $dev)/driver) 2>/dev/null || echo "none")
    echo "  $addr: driver=$driver totalvfs=$total numvfs=$numvfs"
  fi
done
echo "=== virtfn symlinks ==="
for dev in /sys/bus/pci/devices/*/virtfn0; do
  if [ -L "$dev" ]; then
    pf_dir=$(dirname $dev)
    count=$(ls ${pf_dir}/virtfn* 2>/dev/null | wc -l)
    echo "  $(basename $pf_dir) has $count VFs"
  fi
done
echo "=== create-sriov-vfs service status ==="
systemctl status create-sriov-vfs.service --no-pager 2>&1 | head -10 || true
VERIFY_EOF
}

verify_vfs_and_restart_driver() {
  local -a sriov_hosts
  if $SINGLE_NODE; then
    sriov_hosts=("${cluster_name}-ctlplane-0")
  else
    for ((num=0; num<NUM_OF_WORKERS; num++)); do
      sriov_hosts+=("${cluster_name}-worker-${num}")
    done
  fi

  for host in "${sriov_hosts[@]}"; do
    verify_vfs_on_host "$host"
  done

  echo "## restart DRA driver pods so they discover newly created VFs"
  kubectl -n dra-driver-sriov delete pod -l app.kubernetes.io/name=dra-driver-sriov-chart \
    --force --grace-period=0 || true

  echo "## wait for DRA driver pod to be ready again"
  sleep 10
  kubectl -n dra-driver-sriov wait --for=condition=ready pod \
    -l app.kubernetes.io/name=dra-driver-sriov-chart --timeout=120s
}

echo "## Checking requirements"
check_requirements

if [[ "$cleanup_only" == true ]]; then
  echo "## Only cleaning up existing cluster"
  cleanup
  exit 0
fi

echo "## Cleaning up existing cluster"
cleanup

echo "## Creating networks"
create_networks
write_cluster_plan

kcli create cluster generic --paramfile "./${cluster_name}-plan.yaml" "$cluster_name"

export KUBECONFIG="$HOME/.kcli/clusters/$cluster_name/auth/kubeconfig"
if [[ ! -f "${KUBECONFIG}" ]]; then
  echo "Cluster bootstrap failed: missing kubeconfig at ${KUBECONFIG}"
  exit 1
fi
export PATH=$PWD:$PATH

echo "## Waiting for cluster to be ready"
wait_for_cluster_ready

echo "## Labeling nodes"
label_nodes

echo "## Configuring hosts and rebooting"
get_controller_ip
configure_host_registry
configure_hosts_and_reboot

echo "## Waiting for nodes to be ready after reboot"
wait_for_nodes_after_reboot

# Single-node reboots the control plane VM, which can leave a stale cni0 bridge
# without Flannel's 10.85.0.0/16 pod network. Multi-node only reboots workers,
# so this fix is not needed (and would target the wrong node anyway).
if $SINGLE_NODE; then
  echo "## Fixing cni0 bridge for single-node cluster"
  fix_cni_bridge
fi

echo "## Recovering pod networking"
recover_pod_networking

if ! $SINGLE_NODE; then
  deploy_cert_manager
fi

echo "## Deploying internal registry"
deploy_internal_registry

echo "## Building and pushing driver image"
build_and_push_driver_image

echo "## Deploying DRA driver"
deploy_dra_driver
wait_for_dra_driver_daemonset

verify_vfs_and_restart_driver
echo "## Cluster deployed successfully"

echo "## KUBECONFIG=${KUBECONFIG}"
echo "## Controller IP: ${controller_ip}"
echo "## Driver image: ${SRIOV_DRIVER_IMAGE}"
