#!/usr/bin/env python3
"""
ci-vm.py  --  CI VM lifecycle management using cloud-hypervisor.

Subcommands:
  create              Create a VM, write envfile, exit.
  destroy ENVFILE     Destroy a VM described by ENVFILE.
  run                 Create a VM, block until SIGTERM/SIGINT, then destroy.

Environment variables:
  NAME        VM name           (default: ci-ubuntu-USER-TIMESTAMP)
  OUTDIR      envfile directory (default: cwd)
  VCPUS       vCPU count        (default: 4)
  RAM_MB      RAM in MiB        (default: 16384)
  WORKDIR     disk image dir    (default: /var/lib/libvirt/images)
  EXEDEV_CACHE snapshot cache   (default: /data/ci-snapshots)
  BRIDGE_IP_PREFIX              (default: 192.168.122)
  VM_BRIDGE   host bridge name  (default: virbr0)
  CLOUD_HYPERVISOR_BIN          (default: cloud-hypervisor)
"""

from __future__ import annotations

import fcntl
import hashlib
import json
import os
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

# ── Configuration ──────────────────────────────────────────────────────────────

SCRIPT_DIR = Path(__file__).parent.resolve()
REPO_ROOT   = SCRIPT_DIR.parent

NAME       = os.environ.get("NAME", f'ci-ubuntu-{os.getenv("USER","ci")}-{time.strftime("%Y%m%d%H%M%S")}')
VCPUS      = int(os.environ.get("VCPUS",        "4"))
RAM_MB     = int(os.environ.get("RAM_MB",        "16384"))
DISK_GB    = int(os.environ.get("DISK_GB",       "80"))
DATA_GB    = int(os.environ.get("DATA_DISK_GB",  "100"))
WORKDIR    = Path(os.environ.get("WORKDIR",      "/var/lib/libvirt/images"))
SSH_PUBKEY = Path(os.environ.get("SSH_PUBKEY",   str(Path.home() / ".ssh/id_ed25519.pub")))
USER_NAME  = os.environ.get("USER_NAME",         "ubuntu")
CACHE_DIR  = Path(os.environ.get("EXEDEV_CACHE", "/data/ci-snapshots"))
BRIDGE     = os.environ.get("VM_BRIDGE",         "virbr0")
BRIDGE_PFX = os.environ.get("BRIDGE_IP_PREFIX",  "192.168.122")
OUTDIR     = Path(os.environ.get("OUTDIR",        ".")).resolve()
CH_BIN     = os.environ.get("CLOUD_HYPERVISOR_BIN", "cloud-hypervisor")


# ── Utilities ──────────────────────────────────────────────────────────────────

def run(*cmd, check=True, **kw):
    flat = [str(c) for c in cmd]
    print(f"+ {' '.join(flat)}", flush=True)
    return subprocess.run(flat, check=check, **kw)


def sudo(*cmd, **kw):
    return run("sudo", *cmd, **kw)


def cp_clone(src: Path, dst: Path) -> None:
    """Copy src->dst using reflink when the filesystem supports it."""
    dst.parent.mkdir(parents=True, exist_ok=True)
    last_err = ""
    for args in [
        ["cp", "--reflink=always", "--sparse=always", "-a", str(src), str(dst)],
        ["cp", "--reflink=auto",   "--sparse=always", "-a", str(src), str(dst)],
        ["cp",                     "--sparse=always", "-a", str(src), str(dst)],
    ]:
        r = subprocess.run(args, capture_output=True)
        if r.returncode == 0:
            return
        last_err = r.stderr.decode(errors="replace").strip()
    for args in [
        ["sudo", "cp", "--reflink=always", "--sparse=always", "-a", str(src), str(dst)],
        ["sudo", "cp",                     "--sparse=always", "-a", str(src), str(dst)],
    ]:
        r = subprocess.run(args, capture_output=True)
        if r.returncode == 0:
            return
        last_err = r.stderr.decode(errors="replace").strip()
    raise RuntimeError(f"cp_clone failed: {src} -> {dst}: {last_err}")


# ── Snapshot / cache helpers ───────────────────────────────────────────────────

def _setup_hash() -> str:
    r = subprocess.run(
        ["git", "rev-parse", "HEAD:ops/"],
        capture_output=True, text=True, check=True, cwd=REPO_ROOT,
    )
    return r.stdout.strip()


def _image_digest() -> str:
    script = SCRIPT_DIR / "get-image-digest.sh"
    if not script.exists():
        return "nodigest"
    arch = "amd64" if os.uname().machine == "x86_64" else "arm64"
    r = subprocess.run([str(script), "ghcr.io/boldsoftware/exeuntu:latest", arch],
                       capture_output=True, text=True)
    if r.returncode != 0:
        return "nodigest"
    d = r.stdout.strip()
    return (d.split(":")[-1] if ":" in d else d)[:20]


def _snapshot_paths(s_hash: str, img_dig: str):
    snap_dir   = CACHE_DIR / f"ci-vm-{s_hash[:20]}-{img_dig}"
    local_base = WORKDIR  / f"ci-base-{s_hash[:12]}-{img_dig[:12]}.qcow2"
    local_data = WORKDIR  / f"ci-data-{s_hash[:12]}-{img_dig[:12]}.raw"
    return snap_dir, local_base, local_data


# ── IP / MAC allocation ────────────────────────────────────────────────────────

def allocate_ip() -> str:
    """Atomically allocate an IP in {BRIDGE_PFX}.100-199."""
    lock_path    = f"/tmp/ci-vm-ip.lock-{os.getenv('USER','ci')}"
    counter_path = Path(f"/tmp/ci-vm-ip-counter-{os.getenv('USER','ci')}")
    with open(lock_path, "w") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        try:
            octet = int(counter_path.read_text().strip())
        except (OSError, ValueError):
            octet = 100
        ip = f"{BRIDGE_PFX}.{octet}"
        counter_path.write_text(str((octet - 100 + 1) % 100 + 100))
    return ip


def mac_for_ip(ip: str) -> str:
    octet = int(ip.split(".")[-1])
    return f"52:54:00:c0:a8:{octet:02x}"


def tap_name_for(name: str) -> str:
    """Deterministic 15-char TAP interface name derived from VM name."""
    h = hashlib.sha256(name.encode()).hexdigest()
    return f"vm{h[:13]}"


# ── TAP / bridge setup ─────────────────────────────────────────────────────────

def _setup_tap(tap: str) -> None:
    """Create TAP interface and attach it to BRIDGE."""
    r = subprocess.run(["ip", "link", "show", BRIDGE], capture_output=True)
    if r.returncode != 0:
        subprocess.run(["sudo", "virsh", "net-start", "default"],
                       check=False, capture_output=True)
    subprocess.run(["sudo", "ip", "link", "set", BRIDGE, "up"],
                   check=False, capture_output=True)

    sudo("ip", "tuntap", "add", "mode", "tap", tap)
    sudo("ip", "link", "set", tap, "master", BRIDGE)
    sudo("ip", "link", "set", tap, "up")
    print(f"TAP {tap!r} attached to {BRIDGE!r}", flush=True)


def _teardown_tap(tap: str) -> None:
    """Remove a TAP interface (best-effort)."""
    subprocess.run(["sudo", "ip", "link", "del", tap],
                   check=False, capture_output=True)


def _find_product_kernel() -> str | None:
    """Return path to the product kernel (exelet/kernel) if available.

    This is the same kernel used inside exe.dev VMs: 6.12.x with ZFS,
    nftables, virtio, and everything built-in.  No initramfs needed.
    """
    product = REPO_ROOT / "exelet" / "fs" / "amd64" / "kernel" / "kernel"
    if product.exists():
        return str(product)
    print(f"Product kernel not found at {product}", flush=True)
    return None


def _find_kernel(snap_dir: Path) -> tuple[str, str | None]:
    """Return (vmlinuz, initrd_or_None).

    Looks for snapshot-cached kernel with or without initrd.
    Docker-based snapshots have product kernel (no initrd).
    """
    snap_vmlinuz = snap_dir / "vmlinuz"
    snap_initrd  = snap_dir / "initrd.img"
    if snap_vmlinuz.exists() and snap_initrd.exists():
        return str(snap_vmlinuz), str(snap_initrd)
    if snap_vmlinuz.exists():
        return str(snap_vmlinuz), None
    raise RuntimeError(f"Kernel not found in snapshot: {snap_dir}")


# ── VM launch ──────────────────────────────────────────────────────────────────

def _launch_ch(disk: Path, data_disk: Path,
               tap: str, mac: str, ip: str,
               snap_dir: Path) -> int:
    """Start cloud-hypervisor in the background; return its PID."""
    vmlinuz, initrd = _find_kernel(snap_dir)
    log     = f"/tmp/ch-{NAME}.log"
    pidfile = f"/tmp/ch-pid-{NAME}"
    api     = f"/tmp/ch-api-{NAME}.sock"

    gateway = f"{BRIDGE_PFX}.1"
    # Without initramfs (product kernel), root=LABEL= won't resolve;
    # use /dev/vda1 which is the Ubuntu cloud image root partition.
    root_dev = "/dev/vda1" if initrd is None else "LABEL=cloudimg-rootfs"
    cmdline = (
        f"console=ttyS0 root={root_dev} rw"
        " systemd.mask=multipathd.service systemd.mask=multipathd.socket"
    )
    # Kernel boot optimizations for CI snapshot boots:
    # - quiet/loglevel: suppress console spam (~hundreds of log lines)
    # - audit=0: disable audit subsystem
    # - raid=noautodetect: skip MD RAID scanning
    # - mitigations=off: skip Spectre/Meltdown mitigations (ephemeral CI VMs)
    # - rd.udev.log_level: quiet initramfs device discovery
    cmdline += (
        " quiet loglevel=2 audit=0"
        " raid=noautodetect"
        " mitigations=off"
        " rd.udev.log_level=2"
    )
    # Static IP via kernel cmdline.
    # Product kernel (no initramfs/udev) uses traditional eth0 naming;
    # Ubuntu kernel with initramfs uses systemd predictable naming (ens4).
    iface = "eth0" if initrd is None else "ens4"
    cmdline += f" ip={ip}::{gateway}:255.255.255.0:{NAME}:{iface}:off"
    # Mask all services not needed for CI snapshot boots.
    for svc in [
        # cloud-init stack (all four services)
        "cloud-init.service",
        "cloud-init-local.service",
        "cloud-config.service",
        "cloud-final.service",
        # systemd services not needed in CI
        "systemd-sysctl.service",
        "systemd-udev-settle.service",
        "systemd-pstore.service",
        # ZFS share (no NFS/SMB exports)
        "zfs-share.service",
        # Snap/package management
        "snapd.service",
        "snapd.socket",
        "snapd.seeded.service",
        "unattended-upgrades.service",
        # Hardware services irrelevant in a VM
        "ModemManager.service",
        "thermald.service",
        "fwupd.service",
        "fwupd-refresh.timer",
        "secureboot-db.service",
        # VMware guest agents (we use cloud-hypervisor)
        "open-vm-tools.service",
        "vgauth.service",
        # Storage stacks not used
        "open-iscsi.service",
        "iscsid.service",
        "lvm2-monitor.service",
        # Network dispatcher (static IP via cmdline)
        "networkd-dispatcher.service",
        # networkd-wait-online blocks boot when kernel ip= configures
        # the interface outside of networkd's control
        "systemd-networkd-wait-online.service",
        # EFI mount — product kernel lacks CONFIG_VFAT_FS
        "boot-efi.mount",
        # Security/boot not needed in ephemeral CI
        "apparmor.service",
        "grub-initrd-fallback.service",
        "ua-reboot-cmds.service",
        # Plymouth splash (no display)
        "plymouth-quit-wait.service",
        "plymouth-quit.service",
    ]:
        cmdline += f" systemd.mask={svc}"

    ch_args = [
        CH_BIN,
        "--kernel",    vmlinuz,
    ]
    if initrd is not None:
        ch_args += ["--initramfs", initrd]
    ch_args += [
        "--cmdline",   cmdline,
        "--disk",
        f"path={disk}",
        f"path={data_disk}",
        "--net",       f"tap={tap},mac={mac}",
        "--cpus",      f"boot={VCPUS}",
        "--memory",    f"size={RAM_MB}M",
        "--api-socket", api,
        "--serial",    f"file={log}",
        "--console",   "off",
    ]

    quoted = " ".join(shlex.quote(str(a)) for a in ch_args)
    shell  = f"nohup {quoted} >> {shlex.quote(log)} 2>&1 & echo $! > {shlex.quote(pidfile)}"
    sudo("bash", "-c", shell)

    for _ in range(20):
        try:
            pid = int(Path(pidfile).read_text().strip())
            print(f"cloud-hypervisor PID: {pid}", flush=True)
            return pid
        except (OSError, ValueError):
            time.sleep(0.1)
    raise RuntimeError(f"cloud-hypervisor did not write PID to {pidfile}")


# ── Wait for SSH ───────────────────────────────────────────────────────────────

def _wait_ssh(ip: str, timeout: float = 120) -> None:
    """Wait until SSH port is open, then until key-auth succeeds."""
    import socket as _sock
    deadline = time.monotonic() + timeout
    attempt  = 0
    while time.monotonic() < deadline:
        try:
            s = _sock.create_connection((ip, 22), timeout=2)
            s.close()
            break
        except OSError:
            attempt += 1
            if attempt % 20 == 0:
                elapsed = int(time.monotonic() - (deadline - timeout))
                print(f"  SSH port not open after {elapsed}s ...", flush=True)
            time.sleep(0.3)
    else:
        raise TimeoutError(f"SSH port at {ip}:22 not open after {timeout:.0f}s")

    print("SSH port open, waiting for key auth ...", flush=True)
    while time.monotonic() < deadline:
        r = subprocess.run(
            ["ssh",
             "-o", "StrictHostKeyChecking=no",
             "-o", "UserKnownHostsFile=/dev/null",
             "-o", "ConnectTimeout=2",
             "-o", "BatchMode=yes",
             "-o", "LogLevel=ERROR",
             f"{USER_NAME}@{ip}", "true"],
            capture_output=True, timeout=10,
        )
        if r.returncode == 0:
            return
        time.sleep(0.3)

    print(f"SSH auth failed after {timeout:.0f}s", flush=True)
    serial_log = Path(f"/tmp/ch-{NAME}.log")
    if serial_log.exists():
        for line in serial_log.read_text().splitlines()[-30:]:
            print(f"  {line}", flush=True)
    raise TimeoutError(f"SSH auth at {ip}:22 not ready after {timeout:.0f}s")


# ── Cloud-hypervisor artifact build ────────────────────────────────────────────

def _ensure_cloud_hypervisor_artifacts(vm_arch: str, ch_version: str,
                                        cache_dir: Path) -> None:
    artifact = cache_dir / f"cloud-hypervisor-{ch_version}-{vm_arch}.tar.gz"
    if artifact.exists():
        print(f"Cloud Hypervisor {ch_version} ({vm_arch}) cache hit", flush=True)
        return

    build_context = SCRIPT_DIR / "cloud-hypervisor"
    if not build_context.exists():
        raise RuntimeError(f"Cloud Hypervisor Docker context missing: {build_context}")

    docker_cmd: list[str] = []
    for candidate in [["docker"], ["sudo", "docker"]]:
        if subprocess.run(candidate + ["info"], capture_output=True).returncode == 0:
            docker_cmd = candidate
            break
    if not docker_cmd:
        raise RuntimeError("Docker is required to build Cloud Hypervisor artifacts")

    platform = {"amd64": "linux/amd64", "arm64": "linux/arm64"}[vm_arch]
    image_tag = f"exe-cloud-hypervisor:{ch_version}-{vm_arch}"
    virtiofsd_version = os.environ.get("VIRTIOFSD_VERSION", "1.13.2")

    print(f"Building Cloud Hypervisor {ch_version} ({vm_arch}) via Docker ...", flush=True)
    run(*docker_cmd, "build",
        "--platform", platform,
        "--tag", image_tag,
        "--build-arg", f"CLOUD_HYPERVISOR_VERSION={ch_version}",
        "--build-arg", f"VIRTIOFSD_VERSION={virtiofsd_version}",
        "--build-arg", f"TARGETARCH={vm_arch}",
        str(build_context))

    container_id = subprocess.check_output(
        docker_cmd + ["create", image_tag, "/bin/true"], text=True).strip()
    tmp_dir = Path(tempfile.mkdtemp())
    try:
        run(*docker_cmd, "cp", f"{container_id}:/out/.", str(tmp_dir))
        run("tar", "czf", str(artifact), "-C", str(tmp_dir), ".")
        artifact.chmod(0o644)
    finally:
        subprocess.run(docker_cmd + ["rm", container_id], capture_output=True)
        shutil.rmtree(str(tmp_dir), ignore_errors=True)


# ── Snapshot creation ──────────────────────────────────────────────────────────

def _ensure_snapshot(snap_dir: Path) -> None:
    """Ensure a cached VM snapshot exists, building one if necessary.

    Uses a file lock so that parallel CI jobs on the same host coordinate:
    only one builds the snapshot while the others wait.
    """
    snap_base = snap_dir / "base.qcow2"
    snap_data = snap_dir / "data.raw"
    if snap_base.exists() and snap_data.exists():
        return

    lock_path = CACHE_DIR / f"{snap_dir.name}.provision.lock"
    sudo("mkdir", "-p", str(CACHE_DIR))
    sudo("touch", str(lock_path))
    sudo("chmod", "666", str(lock_path))
    fd = open(lock_path, "w")
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        if snap_base.exists() and snap_data.exists():
            print(f"Snapshot became available while waiting: {snap_dir}", flush=True)
            return
        _build_snapshot(snap_dir)
    finally:
        fcntl.flock(fd, fcntl.LOCK_UN)
        fd.close()


def _build_snapshot(snap_dir: Path) -> None:
    """Build a snapshot using Docker-based rootfs + product kernel."""
    product_kernel = _find_product_kernel()
    if not product_kernel:
        raise RuntimeError("Product kernel not found. Cannot build snapshot.")
    docker_ctx = SCRIPT_DIR / "ci-rootfs"
    if not docker_ctx.exists():
        raise RuntimeError(f"Docker rootfs context not found: {docker_ctx}")
    if not shutil.which("docker"):
        raise RuntimeError("Docker is required to build snapshots")
    _build_docker_snapshot(snap_dir, product_kernel)


# ── Docker-based snapshot (product kernel) ─────────────────────────────────────

def _build_docker_rootfs(ssh_pubkey: str) -> Path:
    """Build a CI VM rootfs via Docker and return path to the qcow2 image.

    The image is cached based on a hash of the Dockerfile + SSH pubkey.
    """
    docker_ctx = SCRIPT_DIR / "ci-rootfs"
    dockerfile = docker_ctx / "Dockerfile"

    # Cache key based on Dockerfile content
    h = hashlib.sha256()
    h.update(dockerfile.read_bytes())
    h.update(ssh_pubkey.encode())
    cache_key = h.hexdigest()[:16]
    cached = WORKDIR / f"ci-rootfs-{cache_key}.qcow2"
    if cached.exists():
        print(f"Docker rootfs cache hit: {cached}", flush=True)
        return cached

    print("Building Docker rootfs ...", flush=True)
    t0 = time.monotonic()
    image_tag = f"exe-ci-rootfs:{cache_key}"

    sudo("docker", "build", "--network=host",
         "-t", image_tag, str(docker_ctx))

    # Export container filesystem
    container = f"exe-ci-rootfs-export-{os.getpid()}"
    with tempfile.TemporaryDirectory() as td:
        td = Path(td)
        try:
            sudo("docker", "create", "--name", container, image_tag, "/bin/true")
            tarball = td / "rootfs.tar"
            with open(tarball, "w") as f:
                subprocess.run(
                    ["sudo", "docker", "export", container],
                    stdout=f, check=True)
        finally:
            subprocess.run(["sudo", "docker", "rm", container],
                           capture_output=True)

        # Create raw disk with single ext4 partition
        raw = td / "disk.raw"
        sudo("truncate", "-s", f"{DISK_GB}G", str(raw))
        subprocess.run(
            ["sudo", "sfdisk", str(raw)],
            input=",,L\n", text=True, check=True, capture_output=True)

        # Set up loop device
        loop = subprocess.check_output(
            ["sudo", "losetup", "--find", "--show", "--partscan", str(raw)],
            text=True).strip()
        part = f"{loop}p1"
        try:
            for _ in range(40):
                if Path(part).exists():
                    break
                time.sleep(0.1)
            else:
                raise RuntimeError(f"Partition {part} not found")

            sudo("mkfs.ext4", "-L", "cloudimg-rootfs", "-q", part)
            mnt = td / "mnt"
            sudo("mkdir", "-p", str(mnt))
            sudo("mount", part, str(mnt))
            try:
                sudo("tar", "xf", str(tarball), "-C", str(mnt))
                fstab = "LABEL=cloudimg-rootfs / ext4 defaults 0 1\n"
                subprocess.run(
                    ["sudo", "tee", str(mnt / "etc" / "fstab")],
                    input=fstab, text=True, check=True, capture_output=True)
                ssh_dir = mnt / "home" / "ubuntu" / ".ssh"
                sudo("mkdir", "-p", str(ssh_dir))
                subprocess.run(
                    ["sudo", "tee", str(ssh_dir / "authorized_keys")],
                    input=ssh_pubkey + "\n", text=True, check=True,
                    capture_output=True)
                sudo("chmod", "700", str(ssh_dir))
                sudo("chmod", "600", str(ssh_dir / "authorized_keys"))
                sudo("chown", "-R", "1000:1000", str(ssh_dir))
                # Docker manages /etc/hosts and /etc/resolv.conf during
                # build so we write them after extraction.
                hosts = "127.0.0.1 localhost\n127.0.1.1 ci-vm\n"
                subprocess.run(
                    ["sudo", "tee", str(mnt / "etc" / "hosts")],
                    input=hosts, text=True, check=True,
                    capture_output=True)
                sudo("rm", "-f", str(mnt / "etc" / "resolv.conf"))
                sudo("ln", "-s",
                     "/run/systemd/resolve/stub-resolv.conf",
                     str(mnt / "etc" / "resolv.conf"))
            finally:
                sudo("umount", str(mnt))
        finally:
            sudo("losetup", "-d", loop)

        # Convert to qcow2
        tmp_qcow2 = Path(str(cached) + ".converting")
        sudo("qemu-img", "convert", "-f", "raw", "-O", "qcow2",
             str(raw), str(tmp_qcow2))
        sudo("mv", str(tmp_qcow2), str(cached))

    dt = time.monotonic() - t0
    print(f"Docker rootfs built in {dt:.1f}s: {cached}", flush=True)
    return cached


def _build_docker_snapshot(snap_dir: Path, product_kernel: str) -> None:
    """Build a snapshot using Docker rootfs + product kernel.

    Much faster than cloud-init: no cloud-init wait, no apt-get during
    provision, product kernel boots without initramfs, no kernel extraction.
    """
    print(f"Building Docker-based snapshot: {snap_dir.name}", flush=True)
    t0 = time.monotonic()
    ssh_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]
    scp_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]

    ssh_pubkey = SSH_PUBKEY.read_text().strip()
    rootfs = _build_docker_rootfs(ssh_pubkey)

    ip  = allocate_ip()
    mac = mac_for_ip(ip)
    tap = tap_name_for(NAME + "-snap")
    disk      = WORKDIR / f"{NAME}-snap.qcow2"
    data_disk = WORKDIR / f"{NAME}-snap-data.raw"

    # Create overlay disk backed by the rootfs
    sudo("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
         "-b", str(rootfs), str(disk), f"{DISK_GB}G")
    sudo("truncate", "-s", f"{DATA_GB}G", str(data_disk))
    _setup_tap(tap)

    # Launch CH with product kernel (no initrd).
    log     = f"/tmp/ch-{NAME}.log"
    pidfile = f"/tmp/ch-pid-{NAME}"
    api     = f"/tmp/ch-api-{NAME}.sock"
    gateway = f"{BRIDGE_PFX}.1"
    cmdline = (
        f"console=ttyS0 root=/dev/vda1 rw"
        f" ip={ip}::{gateway}:255.255.255.0:{NAME}:eth0:off"
        " systemd.mask=multipathd.service systemd.mask=multipathd.socket"
    )
    ch_args = [
        CH_BIN,
        "--kernel",    product_kernel,
        "--cmdline",   cmdline,
        "--disk",
        f"path={disk}",
        f"path={data_disk}",
        "--net",       f"tap={tap},mac={mac}",
        "--cpus",      f"boot={VCPUS}",
        "--memory",    f"size={RAM_MB}M",
        "--api-socket", api,
        "--serial",    f"file={log}",
        "--console",   "off",
    ]

    quoted = " ".join(shlex.quote(str(a)) for a in ch_args)
    shell  = f"nohup {quoted} >> {shlex.quote(log)} 2>&1 & echo $! > {shlex.quote(pidfile)}"
    sudo("bash", "-c", shell)

    for _ in range(20):
        try:
            pid = int(Path(pidfile).read_text().strip())
            print(f"cloud-hypervisor PID: {pid}", flush=True)
            break
        except (OSError, ValueError):
            time.sleep(0.1)
    else:
        raise RuntimeError(f"cloud-hypervisor did not write PID to {pidfile}")

    try:
        _wait_ssh(ip, timeout=120)
        dt_boot = time.monotonic() - t0
        print(f"SSH ready (Docker snapshot VM) in {dt_boot:.1f}s.", flush=True)

        _provision_docker_vm(ip, ssh_opts, scp_opts)
        _save_docker_snapshot(ip, disk, data_disk, snap_dir,
                              product_kernel, ssh_opts)
    finally:
        subprocess.run(["sudo", "kill", str(pid)], capture_output=True)
        for f in [disk, data_disk]:
            subprocess.run(["sudo", "rm", "-f", str(f)], capture_output=True)
        _teardown_tap(tap)

    dt = time.monotonic() - t0
    print(f"Docker snapshot built in {dt:.1f}s", flush=True)


def _provision_docker_vm(ip: str, ssh_opts: list, scp_opts: list) -> None:
    """Provision a Docker-based VM: install CH + exelet, create ZFS pool, preload images.

    Skips cloud-init wait and apt installs (everything baked into Docker rootfs).
    """
    vm_arch_raw = subprocess.check_output(
        ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", "uname -m"], text=True).strip()
    vm_arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(vm_arch_raw, vm_arch_raw)

    ch_version = os.environ.get("CLOUD_HYPERVISOR_VERSION", "48.0")
    cache_dir  = Path.home() / ".cache" / "exedops"
    cache_dir.mkdir(parents=True, exist_ok=True)

    _ensure_cloud_hypervisor_artifacts(vm_arch, ch_version, cache_dir)

    exeletd_bin   = cache_dir / f"exeletd-{vm_arch}"
    exeletctl_bin = cache_dir / f"exelet-ctl-{vm_arch}"
    if not exeletd_bin.exists() or not exeletctl_bin.exists():
        print(f"Building exeletd and exelet-ctl for {vm_arch} ...", flush=True)
        go_env = {**os.environ, "GOOS": "linux", "GOARCH": vm_arch}
        run("make", f"GOARCH={vm_arch}", "exe-init", cwd=REPO_ROOT)
        run("go", "build", "-o", str(exeletd_bin), "./cmd/exelet",
            env=go_env, cwd=REPO_ROOT)
        run("go", "build", "-o", str(exeletctl_bin), "./cmd/exelet-ctl",
            env=go_env, cwd=REPO_ROOT)

    ch_artifact  = cache_dir / f"cloud-hypervisor-{ch_version}-{vm_arch}.tar.gz"
    setup_ch     = SCRIPT_DIR / "deploy" / "setup-cloud-hypervisor.sh"
    setup_exelet = SCRIPT_DIR / "setup-exelet.sh"

    run("scp", *scp_opts, str(setup_ch), str(setup_exelet), f"{USER_NAME}@{ip}:~/")
    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}", "mkdir -p ~/.cache/exedops")
    run("scp", *scp_opts,
        str(ch_artifact), str(exeletd_bin), str(exeletctl_bin),
        f"{USER_NAME}@{ip}:~/.cache/exedops/")

    # Copy Docker Hub auth for authenticated pulls.
    docker_cfg = Path.home() / ".docker" / "config.json"
    if docker_cfg.exists():
        run("scp", *scp_opts, str(docker_cfg), f"{USER_NAME}@{ip}:/tmp/docker-config.json")
        run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
            "sudo mkdir -p /root/.docker && sudo mv /tmp/docker-config.json /root/.docker/config.json")
    elif os.environ.get("BUILDKITE"):
        raise RuntimeError(
            "~/.docker/config.json not found. Docker Hub pulls will be rate-limited.\n"
            f"Fix: sudo -u buildkite-agent docker login\nExpected path: {docker_cfg}")

    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "sudo mv ~/setup-cloud-hypervisor.sh ~/setup-exelet.sh /root/ "
        "&& sudo chmod +x /root/setup-cloud-hypervisor.sh /root/setup-exelet.sh")

    # Set up ZFS pool (normally done by cloud-init runcmd).
    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "sudo udevadm settle --timeout=5 2>/dev/null || true; "
        "sudo zpool create -f -m none tank /dev/vdb 2>/dev/null || { "
        "  echo 'Whole-disk zpool failed, partitioning manually...'; "
        "  echo ',,L' | sudo sfdisk /dev/vdb; "
        "  sudo udevadm trigger --subsystem-match=block; "
        "  sudo udevadm settle --timeout=5; "
        "  sudo zpool create -f -m none tank /dev/vdb1; "
        "}; "
        "sudo zfs create -o mountpoint=/data tank/data")

    # Hugepages and DNS.
    gateway = f"{BRIDGE_PFX}.1"
    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "HP=$(awk '/MemTotal/{print int($2/4096)}' /proc/meminfo) && "
        "echo $HP | sudo tee /proc/sys/vm/nr_hugepages >/dev/null; "
        f"sudo mkdir -p /etc/systemd/resolved.conf.d && "
        f"printf '[Resolve]\\nDNS={gateway}\\n' | "
        f"sudo tee /etc/systemd/resolved.conf.d/ci.conf >/dev/null && "
        "sudo systemctl restart systemd-resolved && "
        "for i in $(seq 1 20); do getent hosts github.com >/dev/null 2>&1 && break; sleep 0.5; done")

    print("Running setup-cloud-hypervisor.sh ...", flush=True)
    run("ssh", *ssh_opts, "-o", "LogLevel=ERROR", f"{USER_NAME}@{ip}",
        "sudo /bin/bash -x /root/setup-cloud-hypervisor.sh")

    print("Running setup-exelet.sh ...", flush=True)
    run("ssh", *ssh_opts, "-o", "LogLevel=ERROR", f"{USER_NAME}@{ip}",
        "sudo /bin/bash -x /root/setup-exelet.sh")


def _save_docker_snapshot(ip: str, disk: Path, data_disk: Path,
                          snap_dir: Path, product_kernel: str,
                          ssh_opts: list) -> None:
    """Save a Docker-based snapshot: disk images + product kernel."""
    staging = Path(str(snap_dir) + ".staging")
    sudo("rm", "-rf", str(staging))
    sudo("mkdir", "-p", str(staging))
    sudo("chmod", "755", str(staging))

    # Export ZFS pool cleanly.
    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "sudo zpool export tank 2>/dev/null || true; sudo sync")

    # Copy product kernel to snapshot dir (no initrd needed).
    sudo("cp", product_kernel, str(staging / "vmlinuz"))
    sudo("chmod", "a+r", str(staging / "vmlinuz"))

    cp_clone(disk, staging / "base.qcow2")
    cp_clone(data_disk, staging / "data.raw")
    sudo("chmod", "a+r", str(staging / "base.qcow2"), str(staging / "data.raw"))

    # Atomic rename.
    sudo("mkdir", "-p", str(snap_dir.parent))
    if snap_dir.exists():
        sudo("rm", "-rf", str(snap_dir))
    sudo("mv", str(staging), str(snap_dir))
    print(f"Snapshot cached at {snap_dir}", flush=True)


# ── Destroy ────────────────────────────────────────────────────────────────────

def destroy_vm(envfile: Path) -> None:
    """Destroy a VM. Reads envfile, SIGKILL immediately, clean up."""
    if not envfile.exists():
        return

    env: dict[str, str] = {}
    for line in envfile.read_text().splitlines():
        if "=" in line:
            k, _, v = line.partition("=")
            env[k.strip()] = v.strip()

    name = env.get("VM_NAME", "")
    pid  = env.get("VM_PID", "")
    tap  = env.get("VM_TAP", "")

    if pid:
        # SIGKILL immediately -- no point waiting for graceful shutdown on CI VMs.
        subprocess.run(["sudo", "kill", "-9", pid], check=False, capture_output=True)

    if tap:
        subprocess.run(["sudo", "ip", "link", "del", tap],
                       check=False, capture_output=True)

    for key in ("VM_DISK", "VM_DATA_DISK"):
        path = env.get(key, "")
        if path:
            subprocess.run(["sudo", "rm", "-f", path],
                           check=False, capture_output=True)

    for f in (f"/tmp/ch-{name}.log", f"/tmp/ch-pid-{name}", f"/tmp/ch-api-{name}.sock"):
        subprocess.run(["sudo", "rm", "-f", f], check=False, capture_output=True)

    envfile.unlink(missing_ok=True)


# ── Create VM (core logic) ─────────────────────────────────────────────────────

def create_vm() -> Path:
    """Create a VM. Returns the envfile path."""
    t0 = time.monotonic()
    print(f"══════ ci-vm.py create  NAME={NAME} ══════", flush=True)
    OUTDIR.mkdir(parents=True, exist_ok=True)
    sudo("mkdir", "-p", str(WORKDIR))

    if not SSH_PUBKEY.exists():
        sys.exit(f"SSH pubkey not found: {SSH_PUBKEY}")

    # Compute snapshot key.
    s_hash  = _setup_hash()
    img_dig = _image_digest()

    snap_dir, local_base, local_data = _snapshot_paths(s_hash, img_dig)
    snap_base = snap_dir / "base.qcow2"
    snap_data = snap_dir / "data.raw"

    # ── Ensure snapshot exists (builds one if needed) ──
    _ensure_snapshot(snap_dir)

    # ── Boot from snapshot ──
    disk      = WORKDIR / f"{NAME}.qcow2"
    data_disk = WORKDIR / f"{NAME}-data.raw"
    tap       = tap_name_for(NAME)

    ip  = allocate_ip()
    mac = mac_for_ip(ip)
    print(f"Allocated IP: {ip}  MAC: {mac}", flush=True)

    def _needs_flatten(img: Path) -> bool:
        r = subprocess.run(
            ["qemu-img", "info", "--output=json", str(img)],
            capture_output=True, text=True)
        if r.returncode != 0:
            return True
        info = json.loads(r.stdout)
        return "backing-filename" in info

    def clone_root():
        if not local_base.exists() or _needs_flatten(local_base):
            tmp = Path(str(local_base) + ".converting")
            lock = Path("/tmp") / f"{local_base.name}.lock-{os.getenv('USER', 'ci')}"
            fd = open(lock, "w")
            try:
                fcntl.flock(fd, fcntl.LOCK_EX)
                if not local_base.exists() or _needs_flatten(local_base):
                    print("Converting snapshot base to flat qcow2 ...", flush=True)
                    sudo("qemu-img", "convert", "-f", "qcow2", "-O", "qcow2",
                         str(snap_base), str(tmp))
                    sudo("mv", str(tmp), str(local_base))
            finally:
                fcntl.flock(fd, fcntl.LOCK_UN)
                fd.close()
        sudo("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
             "-b", str(local_base), str(disk))

    def clone_data():
        if not local_data.exists():
            tmp = Path(str(local_data) + ".converting")
            lock = Path("/tmp") / f"{local_data.name}.lock-{os.getenv('USER', 'ci')}"
            fd = open(lock, "w")
            try:
                fcntl.flock(fd, fcntl.LOCK_EX)
                if not local_data.exists():
                    cp_clone(snap_data, tmp)
                    sudo("mv", str(tmp), str(local_data))
            finally:
                fcntl.flock(fd, fcntl.LOCK_UN)
                fd.close()
        cp_clone(local_data, data_disk)

    def setup_tap():
        _setup_tap(tap)

    with ThreadPoolExecutor(max_workers=3) as pool:
        futs = {
            pool.submit(clone_root): "clone_root",
            pool.submit(clone_data): "clone_data",
            pool.submit(setup_tap):  "setup_tap",
        }
        for fut in as_completed(futs):
            task = futs[fut]
            exc  = fut.exception()
            if exc:
                raise RuntimeError(f"Parallel task {task!r} failed: {exc}") from exc

    pid = _launch_ch(disk, data_disk, tap, mac, ip, snap_dir)

    print(f"Waiting for SSH at {ip} (timeout=120s) ...", flush=True)
    _wait_ssh(ip, timeout=120)
    print("SSH ready.", flush=True)

    # Post-boot setup for snapshot boots.
    ssh_opts = ["-o", "StrictHostKeyChecking=no",
                "-o", "UserKnownHostsFile=/dev/null",
                "-o", "LogLevel=ERROR"]
    dns_server = f"{BRIDGE_PFX}.1"
    setup_cmds = (
        "sudo modprobe zfs 2>/dev/null || true; "
        "sudo zpool import -f -N tank 2>/dev/null || true; "
        "HP=$(awk '/MemTotal/{print int($2/4096)}' /proc/meminfo) && "
        "echo $HP | sudo tee /proc/sys/vm/nr_hugepages >/dev/null; "
        f"sudo mkdir -p /etc/systemd/resolved.conf.d && "
        f"echo -e '[Resolve]\\nDNS={dns_server}' | "
        f"sudo tee /etc/systemd/resolved.conf.d/ci.conf >/dev/null && "
        f"sudo resolvectl dns eth0 {dns_server} 2>/dev/null || "
        f"sudo resolvectl dns ens4 {dns_server} 2>/dev/null || "
        f"sudo systemctl restart systemd-resolved"
    )
    r = subprocess.run(
        ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", setup_cmds],
        capture_output=True, text=True, timeout=60)
    if r.returncode != 0:
        print(f"Post-boot setup warning (exit {r.returncode}):", flush=True)
        if r.stdout.strip():
            print(r.stdout.strip(), flush=True)
        if r.stderr.strip():
            print(r.stderr.strip(), flush=True)

    elapsed = time.monotonic() - t0
    print(f"══════ VM ready in {elapsed:.1f}s  NAME={NAME}  IP={ip} ══════", flush=True)

    # Write envfile.
    envfile = OUTDIR / f"{NAME}.env"
    envfile.write_text(
        f"VM_NAME={NAME}\n"
        f"VM_IP={ip}\n"
        f"VM_USER={USER_NAME}\n"
        f"VM_TAP={tap}\n"
        f"VM_PID={pid}\n"
        f"VM_DISK={disk}\n"
        f"VM_DATA_DISK={data_disk}\n"
    )
    print(str(envfile), flush=True)
    return envfile


# ── Subcommands ────────────────────────────────────────────────────────────────

def cmd_create():
    create_vm()


def cmd_destroy():
    if len(sys.argv) < 3:
        sys.exit("usage: ci-vm.py destroy ENVFILE")
    destroy_vm(Path(sys.argv[2]))


def cmd_run():
    """Create VM, block until signal, then destroy."""
    envfile = None
    try:
        envfile = create_vm()

        # Block until SIGTERM or SIGINT.
        stop = False
        def _handle_signal(signum, frame):
            nonlocal stop
            stop = True
        signal.signal(signal.SIGTERM, _handle_signal)
        signal.signal(signal.SIGINT, _handle_signal)

        print("VM running. Send SIGTERM or SIGINT to destroy.", flush=True)
        while not stop:
            time.sleep(0.5)
    finally:
        if envfile:
            destroy_vm(envfile)


def cmd_ensure_snapshot():
    """Ensure the CI snapshot exists, building it if needed.

    This is meant to run as a separate pipeline step so snapshot
    provisioning overlaps with binary builds.
    """
    sudo("mkdir", "-p", str(WORKDIR))

    s_hash  = _setup_hash()
    img_dig = _image_digest()

    snap_dir, _, _ = _snapshot_paths(s_hash, img_dig)
    _ensure_snapshot(snap_dir)
    print(f"Snapshot ready: {snap_dir}", flush=True)


def main():
    if len(sys.argv) < 2:
        sys.exit("usage: ci-vm.py {create|destroy|run|ensure-snapshot} [args]")

    cmd = sys.argv[1]
    if cmd == "create":
        cmd_create()
    elif cmd == "destroy":
        cmd_destroy()
    elif cmd == "run":
        cmd_run()
    elif cmd == "ensure-snapshot":
        cmd_ensure_snapshot()
    else:
        sys.exit(f"unknown subcommand: {cmd}")


if __name__ == "__main__":
    main()
