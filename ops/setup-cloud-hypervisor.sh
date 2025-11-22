#!/bin/sh
set -eu

ASSETS_DIR="/home/ubuntu/.cache/exedops"
CLOUD_HYPERVISOR_VERSION="48.0"

echo "=== Installing cached Cloud Hypervisor binaries ==="

case "$(uname -m)" in
aarch64) ARTIFACT_ARCH="arm64" ;;
x86_64) ARTIFACT_ARCH="amd64" ;;
*)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

ARCHIVE="${ASSETS_DIR}/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-${ARTIFACT_ARCH}.tar.gz"

if [ ! -f "$ARCHIVE" ]; then
    echo "Missing cached Cloud Hypervisor archive: ${ARCHIVE}" >&2
    exit 1
fi

if ! dpkg -s libcap-ng0 libseccomp2 >/dev/null 2>&1; then
    apt update && apt install -y libcap-ng0 libseccomp2
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

tar xzf "$ARCHIVE" -C "$tmp_dir"

for bin in cloud-hypervisor ch-remote virtiofsd; do
    if [ ! -f "${tmp_dir}/bin/${bin}" ]; then
        echo "Cached archive missing ${bin}" >&2
        exit 1
    fi
done

install -m 0755 "${tmp_dir}/bin/cloud-hypervisor" /usr/local/bin/cloud-hypervisor
install -m 0755 "${tmp_dir}/bin/ch-remote" /usr/local/bin/ch-remote
install -m 0755 "${tmp_dir}/bin/virtiofsd" /usr/local/bin/virtiofsd

echo "✓ Installed Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${ARTIFACT_ARCH})"
