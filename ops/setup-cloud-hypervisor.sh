#!/bin/sh
ASSETS_DIR="/home/ubuntu/.cache/exedops"
CLOUD_HYPERVISOR_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"

echo "=== Running setup-cloud-hypervisor.sh ==="

# dependencies
apt update && apt install -y libcap-ng-dev libseccomp-dev git rustup build-essential

# rust tooling
rustup default stable
rustup target add $(uname -m)-unknown-linux-musl

# cloud-hypervisor
cd /tmp
rm -rf cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}
tar zxvf "${ASSETS_DIR}/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}.tar.gz"
cd cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}
cargo build --release
cp -f ./target/release/cloud-hypervisor /usr/local/bin/cloud-hypervisor
cp -f ./target/release/ch-remote /usr/local/bin/ch-remote

# virtiofsd
cd /tmp
rm -rf virtiofsd-v${VIRTIOFSD_VERSION}-*
tar zxvf "${ASSETS_DIR}/virtiofsd-${VIRTIOFSD_VERSION}.tar.gz"
cd virtiofsd-v${VIRTIOFSD_VERSION}-*
cargo build --release
cp -f ./target/release/virtiofsd /usr/local/bin/virtiofsd
