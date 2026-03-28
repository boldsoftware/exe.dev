# Minimal CI VM rootfs for e1e tests.
# Built with Docker, exported as a raw ext4 disk image.
# Booted with the product kernel (no initramfs needed).
#
# This replaces the Ubuntu cloud image + cloud-init provisioning flow.
# Instead of: download cloud image → boot → cloud-init → apt install → setup scripts
# We get:     docker build → export → dd to disk → boot with product kernel

FROM ubuntu:24.04

SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]
ENV DEBIAN_FRONTEND=noninteractive

# Install only what the CI VM actually needs:
#   - openssh-server: for SSH access from test harness
#   - zfsutils-linux: ZFS userspace tools (kernel has ZFS built-in)
#   - socat: used by exelet for vsock proxying
#   - iptables: used by exelet NAT networking
#   - iproute2: ip command for network setup
#   - systemd: init system
#   - libcap-ng0 libseccomp2: cloud-hypervisor runtime deps
#   - ca-certificates: HTTPS for container image pulls
#   - curl: health checks
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        openssh-server \
        zfsutils-linux \
        socat \
        iptables \
        iproute2 \
        systemd systemd-sysv systemd-resolved \
        libcap-ng0 libseccomp2 \
        ca-certificates \
        curl \
        sudo \
        kmod \
        dbus \
        fdisk \
        udev \
    && rm -rf /var/lib/apt/lists/*

# Configure the ubuntu user (already exists in ubuntu:24.04)
# SSH pubkey is injected post-extraction in ci-vm.py, not during Docker build.
RUN usermod -aG sudo ubuntu && \
    echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers && \
    mkdir -p /home/ubuntu/.ssh && \
    chmod 700 /home/ubuntu/.ssh && \
    chown -R ubuntu:ubuntu /home/ubuntu/.ssh

# Configure SSH
RUN mkdir -p /run/sshd && \
    sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config

# Disable all services we don't need (product kernel has everything built-in)
RUN systemctl mask -- \
        cloud-init.service cloud-init-local.service cloud-config.service cloud-final.service \
        snapd.service snapd.socket snapd.seeded.service \
        unattended-upgrades.service \
        apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service \
        motd-news.timer motd-news.service \
        fwupd.service fwupd-refresh.timer \
        ModemManager.service thermald.service \
        secureboot-db.service \
        open-vm-tools.service vgauth.service \
        open-iscsi.service iscsid.service \
        lvm2-monitor.service \
        networkd-dispatcher.service \
        systemd-networkd-wait-online.service \
        apparmor.service \
        grub-initrd-fallback.service \
        ua-reboot-cmds.service \
        plymouth-quit-wait.service plymouth-quit.service \
        multipathd.service multipathd.socket \
        systemd-sysctl.service \
        systemd-udev-settle.service \
        systemd-pstore.service \
    2>/dev/null || true && \
    systemctl set-default multi-user.target

# Pre-create directories the exelet needs
RUN mkdir -p /local /data/exelet/{storage,network,runtime} && \
    chmod 755 /local

# Configure hugepages sysctl (will be set at boot based on available RAM)
RUN mkdir -p /etc/sysctl.d && \
    echo 'vm.nr_hugepages=0' > /etc/sysctl.d/90-exe-hugepages.conf

# Configure hostname
# Note: /etc/hosts is managed by Docker during build and cannot be modified.
# We write a hosts file to a staging location and copy it in the rootfs export.
RUN echo 'ci-vm' > /etc/hostname

# Ensure SSH starts on boot
RUN systemctl enable ssh.service

# Network config: product kernel uses eth0 (traditional naming, no udev)
RUN mkdir -p /etc/systemd/network && \
    printf '[Match]\nName=eth0\n\n[Network]\nDHCP=no\n' \
    > /etc/systemd/network/10-eth0.network && \
    systemctl enable systemd-networkd.service
# Note: /etc/resolv.conf and /etc/hosts are Docker-managed during build.
# They are fixed up in the rootfs extraction step (ci-vm.py).

# Clean up
RUN rm -rf /tmp/* /var/tmp/* /var/cache/apt/*
