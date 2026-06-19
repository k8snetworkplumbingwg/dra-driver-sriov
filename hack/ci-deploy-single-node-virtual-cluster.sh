#!/usr/bin/env bash
set -xeo pipefail

source hack/common.sh

## Single-node configuration: control plane acts as a worker
NUM_OF_WORKERS=0
total_number_of_nodes=1

export OPERATOR_EXEC=kubectl

check_requirements() {
  for cmd in kcli virsh podman make go; do
    if ! command -v "$cmd" &> /dev/null; then
      echo "$cmd is not available"
      exit 1
    fi
  done
  return 0
}

echo "## checking requirements"
check_requirements
echo "## delete existing cluster name $cluster_name"
kcli delete cluster $cluster_name -y || true
kcli delete network ${cluster_name}-sriov -y || true
kcli delete network ${network_name} -y || true

function cleanup {
  kcli delete cluster $cluster_name -y || true
  kcli delete network ${cluster_name}-sriov -y || true
  kcli delete network ${network_name} -y || true
  sudo rm -f /etc/containers/registries.conf.d/003-${cluster_name}.conf
}

if [ -z "$SKIP_DELETE" ]; then
  trap cleanup EXIT
fi

kcli create network -c 192.168.120.0/24 ${network_name}
kcli create network -c 192.168.${virtual_router_id}.0/24 --nodhcp -i ${cluster_name}-sriov

cat <<EOF > ./${cluster_name}-plan.yaml
version: $cluster_version
ctlplane_memory: 10240
worker_memory: 4096
pool: default
disk_size: 30
network: ${network_name}
api_ip: $api_ip
virtual_router_id: $virtual_router_id
domain: $domain_name
ctlplanes: 1
workers: 0
ingress: false
machine: q35
engine: crio
sdn: flannel
autolabeller: false
vmrules:
  - $cluster_name-ctlplane-.*:
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
          numa: 0
        - name: ${cluster_name}-sriov
          type: igb
          vfio: true
          noconf: true
          numa: 1
      numcpus: 4
      numa:
        - id: 0
          vcpus: 0,2
          memory: 5120
        - id: 1
          vcpus: 1,3
          memory: 5120

EOF

kcli create cluster generic --paramfile ./${cluster_name}-plan.yaml $cluster_name

export KUBECONFIG=$HOME/.kcli/clusters/$cluster_name/auth/kubeconfig
if [ ! -f "${KUBECONFIG}" ]; then
  echo "Cluster bootstrap failed: missing kubeconfig at ${KUBECONFIG}"
  exit 1
fi
export PATH=$PWD:$PATH

ATTEMPTS=0
MAX_ATTEMPTS=72
ready=false
sleep_time=10

until $ready || [ $ATTEMPTS -eq $MAX_ATTEMPTS ]
do
    echo "waiting for cluster to be ready"
    if [ $(kubectl get nodes --no-headers | grep -c " Ready ") -ge $total_number_of_nodes ]; then
        echo "cluster is ready"
        ready=true
    else
        echo "cluster is not ready yet"
        sleep $sleep_time
    fi
    ATTEMPTS=$((ATTEMPTS+1))
done

if ! $ready; then
    echo "Timed out waiting for cluster to be ready"
    kubectl get nodes
    exit 1
fi

echo "## untaint control plane to allow scheduling workloads"
kubectl taint nodes --all node-role.kubernetes.io/control-plane- || true

echo "## label control plane as sriov capable and as worker"
kubectl label node $cluster_name-ctlplane-0.$domain_name feature.node.kubernetes.io/network-sriov.capable=true --overwrite
kubectl label node $cluster_name-ctlplane-0.$domain_name node-role.kubernetes.io/worker= --overwrite

controller_ip=$(kubectl get node "${cluster_name}-ctlplane-0.${domain_name}" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
if [ -z "$controller_ip" ]; then
    echo "## ERROR: Failed to get controller IP"
    kubectl get nodes -o wide
    exit 1
fi
insecure_registry="[[registry]]
location = \"$controller_ip:5000\"
insecure = true

[aliases]
\"golang\" = \"docker.io/library/golang\"
"

sudo bash -c "cat << EOF > /etc/containers/registries.conf.d/003-${cluster_name}.conf
$insecure_registry
EOF"

function update_host() {
    node_name=$1
    kcli ssh $node_name << EOF
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

echo '[Unit]
Description=create sriov vfs
Before=network-pre.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -ec "for pf in \$(ls -d /sys/bus/pci/devices/*/driver 2>/dev/null | while read d; do readlink -f \$d | grep -q /igb\$ && dirname \$d; done); do addr=\$(basename \$pf); echo 0 > \$pf/sriov_numvfs || true; echo 5 > \$pf/sriov_numvfs; echo Created VFs on \$addr; done"
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

EOF
}

update_host $cluster_name-ctlplane-0
kcli restart vm "$cluster_name-ctlplane-0"

echo "## wait for node after reboot"
sleep 60

ATTEMPTS=0
MAX_ATTEMPTS=40
ready=false
sleep_time=15

until $ready || [ $ATTEMPTS -eq $MAX_ATTEMPTS ]
do
    echo "waiting for API server to be reachable after reboot (attempt $ATTEMPTS)"
    if kubectl get nodes &>/dev/null; then
        echo "API server is reachable, waiting for node ready..."
        if kubectl wait --for=condition=ready node --all --timeout=60s 2>/dev/null; then
            echo "node is ready"
            ready=true
        fi
    else
        sleep $sleep_time
    fi
    ATTEMPTS=$((ATTEMPTS+1))
done

if ! $ready; then
  echo "## node readiness wait failed; collecting diagnostics"
  kubectl get nodes -o wide || true
  kubectl get events -A --sort-by=.lastTimestamp || true
  kubectl describe node "${cluster_name}-ctlplane-0.${domain_name}" || true
  exit 1
fi

## Deploy internal registry
kubectl create namespace container-registry --dry-run=client -o yaml | kubectl apply -f -

echo "## deploy internal registry"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: registry-pv
spec:
  capacity:
    storage: 20Gi
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
      storage: 20Gi
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

export SRIOV_DRIVER_IMAGE="$controller_ip:5000/dra-driver-sriov"

echo "## build driver image"
CONTAINER_TOOL=podman IMAGE_NAME=${SRIOV_DRIVER_IMAGE} make -C deployments/container/
podman push --tls-verify=false "${SRIOV_DRIVER_IMAGE}"
podman rmi -fi ${SRIOV_DRIVER_IMAGE}

# Deploy the dra driver via helm
set +e
make helm
set -e
${root}/bin/helm upgrade -i dra-driver-sriov deployments/helm/dra-driver-sriov/ \
  --namespace dra-driver-sriov --create-namespace \
  --set kubeletPlugin.configurationMode=${DRA_DRIVER_MODE} \
  --set image.repository=${SRIOV_DRIVER_IMAGE}

echo "## Waiting for daemonset to be ready..."
DS_ATTEMPTS=0
DS_MAX_ATTEMPTS=60
while [ $DS_ATTEMPTS -lt $DS_MAX_ATTEMPTS ]; do
    DESIRED=$(kubectl -n dra-driver-sriov get ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo "0")
    READY=$(kubectl -n dra-driver-sriov get ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin -o jsonpath='{.status.numberReady}' 2>/dev/null || echo "0")

    if [ "$DESIRED" != "" ] && [ "$DESIRED" != "0" ] && [ "$DESIRED" = "$READY" ]; then
        echo "## Daemonset is ready ($READY/$DESIRED)"
        break
    fi
    echo "## Waiting for daemonset to be ready ($READY/$DESIRED)..."
    sleep 5
    DS_ATTEMPTS=$((DS_ATTEMPTS+1))
done

if [ $DS_ATTEMPTS -ge $DS_MAX_ATTEMPTS ]; then
    echo "## ERROR: Timed out waiting for daemonset to be ready"
    kubectl -n dra-driver-sriov get ds -o wide || true
    kubectl -n dra-driver-sriov get pods || true
    exit 1
fi

echo "## apply SriovResourcePolicy to advertise all SR-IOV devices"
cat <<EOF | kubectl apply -f -
apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
kind: SriovResourcePolicy
metadata:
  name: all-devices
  namespace: dra-driver-sriov
spec:
  configs:
  - {}
EOF

echo "## verify VFs were created after reboot"
kcli ssh $cluster_name-ctlplane-0 << 'VERIFY_EOF'
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

echo "## restart DRA driver pod so it discovers newly created VFs"
kubectl -n dra-driver-sriov delete pod -l app.kubernetes.io/name=dra-driver-sriov-chart --force --grace-period=0 || true

echo "## wait for DRA driver pod to be ready again"
sleep 10
kubectl -n dra-driver-sriov wait --for=condition=ready pod -l app.kubernetes.io/name=dra-driver-sriov-chart --timeout=120s

echo "## wait for ResourceSlices to be populated with devices"
ATTEMPTS=0
MAX_ATTEMPTS=30
while [ $ATTEMPTS -lt $MAX_ATTEMPTS ]; do
    DEVICE_COUNT=$(kubectl get resourceslices -o jsonpath='{.items[0].spec.devices}' 2>/dev/null | grep -co '"name"' || true)
    if [ -n "$DEVICE_COUNT" ] && [ "$DEVICE_COUNT" -gt "0" ] 2>/dev/null; then
        echo "## ResourceSlices have $DEVICE_COUNT devices published"
        break
    fi
    echo "## Waiting for devices in ResourceSlices (attempt $ATTEMPTS)..."
    sleep 5
    ATTEMPTS=$((ATTEMPTS+1))
done

echo "## Single-node virtual cluster deployed successfully"
echo "## KUBECONFIG=${KUBECONFIG}"
echo "## Controller IP: ${controller_ip}"
echo "## Driver image: ${SRIOV_DRIVER_IMAGE}"
