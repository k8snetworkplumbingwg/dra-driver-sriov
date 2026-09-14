#!/usr/bin/env bash
# Bootstrap a RHEL/Fedora/CentOS host so that
#   make deploy-single-node-virtual-cluster
# can run (kcli + libvirt + podman + helpers).
#
# Usage:
#   sudo ./hack/bootstrap_rhel.sh
#   # or
#   sudo make bootstrap-rhel
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "ERROR: run as root (sudo ./hack/bootstrap_rhel.sh)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Who invoked sudo (for kvm/libvirt group membership). Fall back to root.
TARGET_USER="${SUDO_USER:-root}"
TARGET_HOME="$(getent passwd "${TARGET_USER}" | cut -d: -f6)"
if [[ -z "${TARGET_HOME}" ]]; then
  TARGET_HOME="/root"
fi

echo "## Detecting OS"
if [[ ! -f /etc/os-release ]]; then
  echo "ERROR: /etc/os-release not found; this script targets RHEL-family systems" >&2
  exit 1
fi
# shellcheck source=/dev/null
. /etc/os-release
case "${ID}" in
  rhel|centos|rocky|almalinux|fedora|ol)
    echo "## OS: ${NAME} ${VERSION_ID:-}"
    ;;
  *)
    echo "ERROR: unsupported OS '${ID}'. Expected RHEL/Fedora/CentOS family." >&2
    exit 1
    ;;
esac

if ! command -v dnf >/dev/null 2>&1; then
  echo "ERROR: dnf is required" >&2
  exit 1
fi

echo "## Installing packages"
# RHEL provides the emulator as /usr/libexec/qemu-kvm (qemu-kvm package).
# Fedora also has qemu-system-x86 (/usr/bin/qemu-system-x86_64).
# edk2-ovmf is needed for q35/UEFI guests used by the virtual-cluster plan.
#
# RHEL 10 dropped some Fedora/Debian-named packages (genisoimage, bridge-utils).
# Install what exists; skip the rest.
REQUIRED_PKGS=(
  qemu-kvm
  edk2-ovmf
  libvirt
  libvirt-client
  libvirt-daemon-kvm
  virt-install
  libguestfs-tools
  dnsmasq
  podman
  make
  python3
  python3-pip
  openssh-clients
)
OPTIONAL_PKGS=(
  qemu-system-x86   # Fedora
  genisoimage       # older RHEL / Fedora; RHEL 10 often has xorriso instead
  xorriso           # ISO tooling used by virt-install/kcli when genisoimage is absent
  bridge-utils      # obsolete on RHEL 10 (iproute bridges are enough)
)

pkg_available() {
  local pkg="$1"
  rpm -q "${pkg}" >/dev/null 2>&1 && return 0
  dnf list --available "${pkg}" >/dev/null 2>&1
}

PKGS=("${REQUIRED_PKGS[@]}")
for pkg in "${OPTIONAL_PKGS[@]}"; do
  if pkg_available "${pkg}"; then
    PKGS+=("${pkg}")
  else
    echo "## skipping unavailable package: ${pkg}"
  fi
done

dnf install -y "${PKGS[@]}"

# golang is optional if already present (many lab images ship it)
if ! command -v go >/dev/null 2>&1; then
  echo "## Installing golang"
  dnf install -y golang || dnf install -y go || {
    echo "WARNING: could not install go via dnf; install a Go toolchain before deploying" >&2
  }
fi

# kubectl is used by the deploy script after the cluster is up
if ! command -v kubectl >/dev/null 2>&1; then
  echo "## kubectl not found; install it before deploying (e.g. from kubernetes.io or OpenShift tools)"
fi

echo "## Ensuring KVM device and group membership"
if [[ ! -e /dev/kvm ]]; then
  echo "ERROR: /dev/kvm is missing. Enable hardware virtualization (or nested KVM) and reload the kvm modules." >&2
  exit 1
fi
chmod 666 /dev/kvm 2>/dev/null || true
if [[ "${TARGET_USER}" != "root" ]]; then
  usermod -aG kvm,libvirt "${TARGET_USER}" || true
fi

echo "## Starting libvirt (system URI qemu:///system)"
# Prefer modular daemons on Fedora/newer RHEL; fall back to monolithic libvirtd.
# kcli needs system libvirt with an x86_64 emulator in capabilities — session URI is not enough.
start_libvirt() {
  local modular=0
  local unit
  for unit in virtqemud virtnetworkd virtstoraged virtnodedevd virtlogd; do
    if systemctl list-unit-files "${unit}.socket" 2>/dev/null | grep -q "${unit}.socket"; then
      systemctl enable --now "${unit}.socket"
      modular=1
    fi
    if systemctl list-unit-files "${unit}.service" 2>/dev/null | grep -q "${unit}.service"; then
      systemctl enable --now "${unit}.service" 2>/dev/null || systemctl start "${unit}.service" 2>/dev/null || true
      modular=1
    fi
  done
  if [[ "${modular}" -eq 0 ]] && systemctl list-unit-files libvirtd.service 2>/dev/null | grep -q libvirtd.service; then
    systemctl enable --now libvirtd
    systemctl start virtlogd 2>/dev/null || true
  fi
}
start_libvirt

export LIBVIRT_DEFAULT_URI="${LIBVIRT_DEFAULT_URI:-qemu:///system}"
if ! virsh -c qemu:///system list --all >/dev/null 2>&1; then
  echo "ERROR: virsh cannot talk to qemu:///system. Is libvirt running?" >&2
  systemctl status virtqemud --no-pager 2>/dev/null || true
  systemctl status libvirtd --no-pager 2>/dev/null || true
  exit 1
fi
echo "## libvirt OK ($(virsh -c qemu:///system uri))"

if ! virsh -c qemu:///system capabilities | grep -q "arch name='x86_64'"; then
  echo "ERROR: libvirt capabilities have no x86_64 guest arch (kcli will fail with 'No valid emulator found')" >&2
  virsh -c qemu:///system capabilities | head -80 >&2 || true
  exit 1
fi
if ! virsh -c qemu:///system capabilities | grep -A20 "arch name='x86_64'" | grep -q '<emulator>'; then
  echo "ERROR: no emulator under x86_64 capabilities. Install qemu-kvm / qemu-system-x86." >&2
  exit 1
fi
echo "## x86_64 emulator: $(virsh -c qemu:///system capabilities | grep -A20 "arch name='x86_64'" | grep '<emulator>' | head -1 | tr -d '[:space:]')"

echo "## Ensuring default libvirt storage pool"
mkdir -p /var/lib/libvirt/images
if ! virsh -c qemu:///system pool-info default >/dev/null 2>&1; then
  virsh -c qemu:///system pool-define-as default dir --target /var/lib/libvirt/images
  virsh -c qemu:///system pool-build default
fi
virsh -c qemu:///system pool-start default 2>/dev/null || true
virsh -c qemu:///system pool-autostart default
virsh -c qemu:///system pool-info default

echo "## Ensuring SSH key for kcli (/root/.ssh/id_rsa)"
# kcli requires a usable public key; CI generates /root/.ssh/id_rsa.
if [[ ! -f /root/.ssh/id_rsa ]]; then
  mkdir -p /root/.ssh
  chmod 700 /root/.ssh
  ssh-keygen -t rsa -b 2048 -f /root/.ssh/id_rsa -N ""
  chmod 600 /root/.ssh/id_rsa
  chmod 644 /root/.ssh/id_rsa.pub
fi
if [[ "${TARGET_HOME}" != "/root" && ! -f "${TARGET_HOME}/.ssh/id_rsa" ]]; then
  mkdir -p "${TARGET_HOME}/.ssh"
  chmod 700 "${TARGET_HOME}/.ssh"
  ssh-keygen -t rsa -b 2048 -f "${TARGET_HOME}/.ssh/id_rsa" -N ""
  chown -R "${TARGET_USER}:${TARGET_USER}" "${TARGET_HOME}/.ssh"
fi

echo "## Installing kcli (pip, same pin as CI: >=99,<100)"
python3 -m pip install --upgrade "kcli>=99.0,<100"
# Ensure kcli is on PATH for root and interactive shells
if ! command -v kcli >/dev/null 2>&1; then
  # common pip --user / system locations
  for candidate in /usr/local/bin/kcli /usr/bin/kcli "${TARGET_HOME}/.local/bin/kcli" /root/.local/bin/kcli; do
    if [[ -x "${candidate}" ]]; then
      ln -sfn "${candidate}" /usr/local/bin/kcli
      break
    fi
  done
fi
kcli version

echo "## Writing kcli config (local system libvirt)"
# Client sections must be top-level. Do NOT set host: 127.0.0.1 — that can make
# kcli treat the client oddly; local with only pool uses qemu:///system.
write_kcli_config() {
  local home_dir="$1"
  local owner="$2"
  local cfg_dir="${home_dir}/.kcli"
  local cfg="${cfg_dir}/config.yml"
  mkdir -p "${cfg_dir}"
  cat > "${cfg}" <<'EOF'
default:
  client: local
  pool: default

local:
  pool: default
EOF
  if [[ "${owner}" != "root" ]]; then
    chown -R "${owner}:${owner}" "${cfg_dir}"
  fi
}
write_kcli_config /root root
if [[ "${TARGET_HOME}" != "/root" ]]; then
  write_kcli_config "${TARGET_HOME}" "${TARGET_USER}"
fi

echo "## Making deploy scripts executable"
chmod +x \
  "${REPO_ROOT}/hack/deploy-virtual-k8s-cluster.sh" \
  "${REPO_ROOT}/hack/virtual-cluster-redeploy.sh" \
  "${REPO_ROOT}/hack/bootstrap_rhel.sh"

echo "## Verifying required commands"
missing=0
for cmd in kcli virsh podman make go; do
  if command -v "${cmd}" >/dev/null 2>&1; then
    echo "  OK  ${cmd}: $(command -v "${cmd}")"
  else
    echo "  MISSING  ${cmd}"
    missing=1
  fi
done
if [[ "${missing}" -ne 0 ]]; then
  echo "ERROR: one or more required commands are missing" >&2
  exit 1
fi

# Smoke-test that kcli sees the hypervisor
if ! kcli list host >/dev/null 2>&1 && ! kcli list pool >/dev/null 2>&1; then
  echo "WARNING: kcli may still not see the local hypervisor; try: kcli list pool" >&2
else
  echo "## kcli can reach the local hypervisor"
  kcli list pool || true
fi

cat <<EOF

## Bootstrap complete

Next:
  cd ${REPO_ROOT}
  make deploy-single-node-virtual-cluster-standalone

Notes:
  - Single-node VM wants ~10GiB RAM for the control plane.
  - If you were added to kvm/libvirt groups as a non-root user, log out/in (or newgrp) before running without sudo.
  - kubectl must be on PATH for post-bootstrap cluster steps.
EOF
