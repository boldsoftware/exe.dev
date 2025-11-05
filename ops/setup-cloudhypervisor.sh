#!/bin/sh

apt update && apt install -y libcap-ng-dev libseccomp-dev git rustup

rustup default stable
rustup target add $(uname -m)-unknown-linux-musl

# cloud-hypervisor
cd /tmp
if [ ! -e "cloud-hypervisor" ]; then
    git clone https://github.com/cloud-hypervisor/cloud-hypervisor
fi
cd cloud-hypervisor
git checkout v48.0
cargo build --release
cp -f ./target/release/cloud-hypervisor /usr/local/bin/cloud-hypervisor
cp -f ./target/release/ch-remote /usr/local/bin/ch-remote

# virtiofsd
cd /tmp
if [ ! -e "virtiofsd" ]; then
    git clone https://gitlab.com/virtio-fs/virtiofsd
fi
cd virtiofsd
cargo build --release
cp -f ./target/release/virtiofsd /usr/local/bin/virtiofsd
