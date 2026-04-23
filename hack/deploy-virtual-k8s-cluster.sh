#!/usr/bin/env bash
set -xeo pipefail

source hack/common.sh

NUM_OF_WORKERS=${NUM_OF_WORKERS:-2}
total_number_of_nodes=$((1 + NUM_OF_WORKERS))

## Global configuration
export OPERATOR_EXEC=kubectl
export MULTUS_NAMESPACE="kube-system"
export CNI_PLUGINS_URL="https://github.com/containernetworking/plugins/releases/download/v1.9.1/cni-plugins-linux-amd64-v1.9.1.tgz"

check_requirements() {
  for cmd in kcli virsh virt-edit podman make go; do
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
kcli delete cluster $cluster_name -y
kcli delete network $cluster_name -y

function cleanup {
  kcli delete cluster $cluster_name -y
  kcli delete network $cluster_name -y
  sudo rm -f /etc/containers/registries.conf.d/003-${cluster_name}.conf
}

if [ -z $SKIP_DELETE ]; then
  trap cleanup EXIT
fi

kcli create network -c 192.168.120.0/24 ${network_name}
kcli create network -c 192.168.${virtual_router_id}.0/24 --nodhcp -i $cluster_name

# TODO: remove this once we have a newer engine version
cat <<EOF > ./${cluster_name}-plan.yaml
version: $cluster_version
ctlplane_memory: 4096
worker_memory: 4096
pool: default
disk_size: 50
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
  - $cluster_name-ctlplane-.*:
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
  - $cluster_name-worker-.*:
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
          numa: 0
        - name: $cluster_name
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

kcli create cluster generic --paramfile ./${cluster_name}-plan.yaml $cluster_name

export KUBECONFIG=$HOME/.kcli/clusters/$cluster_name/auth/kubeconfig
export PATH=$PWD:$PATH

ATTEMPTS=0
MAX_ATTEMPTS=72
ready=false
sleep_time=10

until $ready || [ $ATTEMPTS -eq $MAX_ATTEMPTS ]
do
    echo "waiting for cluster to be ready"
    if [ `kubectl get node | grep Ready | wc -l` == $total_number_of_nodes ]; then
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

function update_worker_labels() {
echo "## label cluster workers as sriov capable"
for ((num=0; num<NUM_OF_WORKERS; num++))
do
    kubectl label node $cluster_name-worker-$num.$domain_name feature.node.kubernetes.io/network-sriov.capable=true --overwrite
done

echo "## label cluster worker as worker"
for ((num=0; num<NUM_OF_WORKERS; num++))
do
  kubectl label node $cluster_name-worker-$num.$domain_name node-role.kubernetes.io/worker= --overwrite
done
}

update_worker_labels

controller_ip=`kubectl get node -o wide | grep ctlp | awk '{print $6}'`
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

mkdir -p /opt/cni/bin
if command -v curl >/dev/null 2>&1; then
  curl -fL --retry 3 --retry-delay 2 --output /tmp/cni-plugins-linux-amd64-v1.9.1.tgz ${CNI_PLUGINS_URL}
elif command -v wget >/dev/null 2>&1; then
  wget -qO /tmp/cni-plugins-linux-amd64-v1.9.1.tgz ${CNI_PLUGINS_URL}
else
  echo "Neither curl nor wget is installed on the node" >&2
  exit 1
fi
tar --overwrite -xzf /tmp/cni-plugins-linux-amd64-v1.9.1.tgz -C /opt/cni/bin
rm -f /tmp/cni-plugins-linux-amd64-v1.9.1.tgz

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
ExecStart=/usr/bin/bash -ec "if command -v ethtool >/dev/null 2>&1; then for nic in eth0 eth1; do [ -d \"/sys/class/net/\$nic\" ] || continue; ethtool --offload \"\$nic\" rx off tx off || true; ethtool -K \"\$nic\" gso off || true; done; fi"
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=default.target' > /etc/systemd/system/disable-offload.service

systemctl daemon-reload
systemctl enable disable-offload.service
systemctl start disable-offload.service || true

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
# Ensure PF devices are present before configuring VFs at boot.
After=systemd-udevd.service
Before=network.target NetworkManager.service

[Service]
Type=oneshot
RemainAfterExit=yes
# Needed on SELinux enforcing hosts where init_t may be blocked from sysfs writes.
SELinuxContext=system_u:system_r:unconfined_service_t:s0
ExecStart=/usr/bin/bash -ec "count=0; for numvfs in /sys/bus/pci/devices/*/sriov_numvfs; do [ -e \"\$numvfs\" ] || continue; dev=\$(dirname \"\$numvfs\"); total=\$(cat \"\$dev/sriov_totalvfs\" 2>/dev/null || echo 0); if [ \"\$total\" -le 0 ]; then continue; fi; target=5; if [ \"\$total\" -lt \"\$target\" ]; then target=\$total; fi; echo 0 > \"\$numvfs\" || true; echo \"\$target\" > \"\$numvfs\"; count=\$((count+1)); done; if [ \"\$count\" -eq 0 ]; then echo \"No SR-IOV PF sriov_numvfs files found\" >&2; fi"
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=multi-user.target' > /etc/systemd/system/create-sriov-vfs.service

restorecon /etc/systemd/system/create-sriov-vfs.service || true
test -s /etc/systemd/system/create-sriov-vfs.service
systemctl daemon-reload
systemctl reenable create-sriov-vfs.service
systemctl start create-sriov-vfs.service || true

systemctl restart NetworkManager

grubby --update-kernel=DEFAULT --args=pci=realloc
grubby --update-kernel=DEFAULT --args=iommu=pt
grubby --update-kernel=DEFAULT --args=intel_iommu=on

EOF
}

update_host $cluster_name-ctlplane-0
for ((num=0; num<NUM_OF_WORKERS; num++))
do
  update_host $cluster_name-worker-$num
  set +e
  kcli ssh $cluster_name-worker-$num sudo reboot
  reboot_rc=$?
  set -e
  if [ "$reboot_rc" -ne 0 ] && [ "$reboot_rc" -ne 255 ]; then
    echo "Failed to reboot $cluster_name-worker-$num (rc=$reboot_rc)"
    exit $reboot_rc
  fi
done

# after the reboot, wait for the nodes to be ready
kubectl wait --for=condition=ready node --all --timeout=10m

# remove the patch after multus bug is fixed
# https://github.com/k8snetworkplumbingwg/multus-cni/issues/1221
kubectl patch  -n ${MULTUS_NAMESPACE} ds/kube-multus-ds --type=json -p='[{"op": "replace", "path": "/spec/template/spec/initContainers/0/command", "value":["cp", "-f","/usr/src/multus-cni/bin/multus-shim", "/host/opt/cni/bin/multus-shim"]}]'

## Deploy internal registry
kubectl create namespace container-registry

echo "## deploy internal registry"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: registry-pv
spec:
  capacity:
    storage: 60Gi
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
      storage: 60Gi
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

export SRIOV_DRIVER_IMAGE="$controller_ip:5000/dra-driver-sriov"

echo "## build driver image"
CONTAINER_TOOL=podman IMAGE_NAME=${SRIOV_DRIVER_IMAGE} make -C deployments/container/
podman push --tls-verify=false "${SRIOV_DRIVER_IMAGE}"
podman rmi -fi ${SRIOV_DRIVER_IMAGE}

# remove the crio bridge and let flannel to recreate
kcli ssh $cluster_name-ctlplane-0 << EOF
sudo su
if [ $(ip a | grep 10.85.0 | wc -l) -eq 0 ]; then ip link del cni0; fi
EOF

kubectl -n ${MULTUS_NAMESPACE} delete po -l name=multus --ignore-not-found=true
kubectl -n kube-system delete po -l k8s-app=kube-dns --ignore-not-found=true

TIMEOUT=400
echo "## wait for coredns"
kubectl -n kube-system wait --for=condition=available deploy/coredns --timeout=${TIMEOUT}s
echo "## wait for multus"
kubectl -n ${MULTUS_NAMESPACE} wait --for=condition=ready -l name=multus pod --timeout=${TIMEOUT}s

echo "## deploy cert manager"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.12.0/cert-manager.yaml

echo "## wait for cert manager to be ready"

ATTEMPTS=0
MAX_ATTEMPTS=72
ready=false
sleep_time=5

until $ready || [ $ATTEMPTS -eq $MAX_ATTEMPTS ]
do
    echo "waiting for cert manager webhook to be ready"
    if [ `kubectl -n cert-manager get po | grep webhook | grep "1/1" | wc -l` == 1 ]; then
        echo "cluster is ready"
        ready=true
    else
        echo "cert manager webhook is not ready yet"
        sleep $sleep_time
    fi
    ATTEMPTS=$((ATTEMPTS+1))
done

# Deploy the dra driver via helm
export PATH=${root}/bin/:$PATH
make helm
${root}/bin/helm upgrade -i dra-driver-sriov deployments/helm/dra-driver-sriov/ --namespace dra-driver-sriov --create-namespace --set kubeletPlugin.configurationMode=${DRA_DRIVER_MODE} --set image.repository=${SRIOV_DRIVER_IMAGE}

# Wait for the daemonset to be fully deployed
echo "## Waiting for daemonset to be ready..."
while true; do
    DESIRED=$(kubectl -n dra-driver-sriov get ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo "0")
    READY=$(kubectl -n dra-driver-sriov get ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin -o jsonpath='{.status.numberReady}' 2>/dev/null || echo "0")
    
    if [ "$DESIRED" != "" ] && [ "$DESIRED" != "0" ] && [ "$DESIRED" = "$READY" ]; then
        echo "## Daemonset is ready ($READY/$DESIRED)"
        break
    fi
    echo "## Waiting for daemonset to be ready ($READY/$DESIRED)..."
    sleep 5
done

echo "## Cluster deployed successfully"
