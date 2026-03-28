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
BASE_IMG   = Path(os.environ.get("BASE_IMG",     "/var/lib/libvirt/images/ubuntu-24.04-base.qcow2"))
BASE_URL   = os.environ.get("BASE_IMG_URL",      "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img")
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


def _base_hash() -> str:
    sidecar = Path(str(BASE_IMG) + ".sha256")
    if sidecar.exists():
        return sidecar.read_text().strip()
    return "nobaseimg"


def _snapshot_paths(s_hash: str, img_dig: str, b_hash: str):
    snap_dir   = CACHE_DIR / f"ci-vm-{s_hash[:20]}-{img_dig}-{b_hash[:12]}"
    local_base = WORKDIR  / f"ci-base-{s_hash[:12]}-{img_dig[:12]}-{b_hash[:12]}.qcow2"
    local_data = WORKDIR  / f"ci-data-{s_hash[:12]}-{img_dig[:12]}-{b_hash[:12]}.raw"
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


# ── Cloud-init ISO ─────────────────────────────────────────────────────────────

def _cloud_init_user_data(pubkey: str, snapshot: bool) -> str:
    """Generate cloud-init user-data as JSON (valid YAML)."""
    if snapshot:
        config = {
            "hostname": NAME,
            "ssh_authorized_keys": [pubkey],
            "users": ["default"],
            "write_files": [{
                "path": "/home/ubuntu/.ssh/authorized_keys",
                "content": pubkey + "\n",
                "owner": "ubuntu:ubuntu",
                "permissions": "0600",
                "defer": True,
            }],
            "package_update": False,
            "bootcmd": [
                ["bash", "-c", "zpool import -f -N tank 2>/dev/null || true"],
            ],
        }
    else:
        config = {
            "hostname": NAME,
            "ssh_authorized_keys": [pubkey],
            "users": ["default"],
            "write_files": [{
                "path": "/home/ubuntu/.ssh/authorized_keys",
                "content": pubkey + "\n",
                "owner": "ubuntu:ubuntu",
                "permissions": "0600",
                "defer": True,
            }],
            "package_update": False,
            "packages": [
                "zfsutils-linux",
                "socat",
                "iptables",
            ],
            "runcmd": [
                "systemctl disable --now apt-daily.timer apt-daily-upgrade.timer || true",
                "systemctl mask apt-daily.service apt-daily-upgrade.service || true",
                "systemctl disable --now motd-news.timer || true",
                "systemctl mask motd-news.service || true",
                "systemctl disable fwupd.service fwupd-refresh.timer || true",
                "mkdir -p /local && chmod 755 /local",
                # Hugepages for inner CH VMs
                "HUGEPAGE_TARGET=$(awk '/MemTotal/ { print int($2/4096); exit(0); }' /proc/meminfo)\n"
                "echo \"$HUGEPAGE_TARGET\" > /proc/sys/vm/nr_hugepages\n"
                "mkdir -p /etc/sysctl.d\n"
                "echo \"vm.nr_hugepages=$HUGEPAGE_TARGET\" > /etc/sysctl.d/90-exe-hugepages.conf",
                # vsock modules for CH
                "modprobe vhost_vsock || true",
                "modprobe vsock || true",
                "echo -e 'vhost_vsock\nvsock' > /etc/modules-load.d/cloud-hypervisor.conf",
                # ZFS pool on data disk
                "if ! zpool list tank >/dev/null 2>&1; then\n"
                "  zpool create -f -m none tank /dev/vdb\n"
                "  zfs create -o mountpoint=/data tank/data\n"
                "fi",
                # Remove stale DHCP netplan
                "rm -f /etc/netplan/60-dhcp-mac.yaml",
                # Speed up networkd-wait-online
                "mkdir -p /etc/systemd/system/systemd-networkd-wait-online.service.d\n"
                "printf '[Service]\\nExecStart=\\nExecStart=/lib/systemd/systemd-networkd-wait-online --any\\n'"
                " > /etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
            ],
            "bootcmd": [
                ["bash", "-c", "rm -f /etc/machine-id /var/lib/dbus/machine-id; systemd-machine-id-setup"],
                ["bash", "-c", "rm -rf /var/lib/systemd/networkd/*"],
                "if [ -b /dev/vdb ]; then\n"
                "  if zpool import 2>/dev/null | grep -q 'pool: tank'; then\n"
                "    zpool import -f -N tank\n"
                "    zpool reguid tank\n"
                "  fi\n"
                "fi",
            ],
        }
    return "#cloud-config\n" + json.dumps(config, indent=2) + "\n"


def _cloud_init_network_config(ip: str, mac: str) -> str:
    """Generate cloud-init network-config as JSON."""
    gateway = f"{BRIDGE_PFX}.1"
    config = {
        "version": 2,
        "ethernets": {
            "id0": {
                "match": {"macaddress": mac},
                "dhcp4": False,
                "dhcp6": False,
                "addresses": [f"{ip}/24"],
                "routes": [{"to": "default", "via": gateway}],
                "nameservers": {"addresses": [gateway]},
            }
        }
    }
    return json.dumps(config, indent=2) + "\n"


def _make_cloud_init_iso(dest: Path, snapshot: bool, ip: str, mac: str) -> None:
    pubkey = SSH_PUBKEY.read_text().strip()

    user_data      = _cloud_init_user_data(pubkey, snapshot)
    network_config = _cloud_init_network_config(ip, mac)
    meta_data      = json.dumps({"instance-id": NAME, "local-hostname": NAME}) + "\n"

    with tempfile.TemporaryDirectory() as td:
        td = Path(td)
        (td / "user-data").write_text(user_data)
        (td / "meta-data").write_text(meta_data)
        (td / "network-config").write_text(network_config)

        files = [td / "user-data", td / "meta-data", td / "network-config"]
        for tool in ("genisoimage", "mkisofs"):
            if shutil.which(tool):
                sudo(tool, "-output", dest, "-volid", "cidata",
                     "-joliet", "-rock", *files)
                return
    raise RuntimeError("Neither genisoimage nor mkisofs found on PATH")


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


# ── Guest kernel extraction ───────────────────────────────────────────────────

def _extract_guest_kernel(disk: Path) -> tuple[str, str]:
    """Extract vmlinuz and initrd from a qcow2 disk image via qemu-nbd.

    Needed for first boot: the host kernel version may not match the guest's
    /lib/modules, so kernel modules (iptables, netfilter) would fail to load.
    """
    cache = WORKDIR / "guest-kernel"
    vmlinuz = cache / "vmlinuz"
    initrd  = cache / "initrd.img"
    if vmlinuz.exists() and initrd.exists():
        return str(vmlinuz), str(initrd)

    sudo("mkdir", "-p", str(cache))
    print(f"Extracting guest kernel from {disk} ...", flush=True)

    flat = WORKDIR / f"{BASE_IMG.stem}-flat.qcow2"
    src = flat if flat.exists() else disk

    nbd_dev = "/dev/nbd0"
    sudo("modprobe", "nbd", "max_part=16")
    sudo("qemu-nbd", "--connect", nbd_dev, "--read-only", str(src))
    sudo("partprobe", nbd_dev)
    time.sleep(1)
    mnt = Path(tempfile.mkdtemp(prefix="guest-kernel-"))
    try:
        import glob as _glob
        partitions = sorted(_glob.glob(f"{nbd_dev}p*"))
        for part in partitions:
            try:
                sudo("mount", "-o", "ro", part, str(mnt))
            except subprocess.CalledProcessError:
                continue
            try:
                for prefix in (mnt / "boot", mnt):
                    vmlinuz_files = sorted(_glob.glob(str(prefix / "vmlinuz-*")))
                    initrd_files  = sorted(_glob.glob(str(prefix / "initrd.img-*")))
                    if vmlinuz_files and initrd_files:
                        sudo("cp", vmlinuz_files[-1], str(vmlinuz))
                        sudo("cp", initrd_files[-1], str(initrd))
                        print(f"Guest kernel: {Path(vmlinuz_files[-1]).name} (from {part})", flush=True)
                        return str(vmlinuz), str(initrd)
            finally:
                sudo("umount", str(mnt))

        raise RuntimeError(f"No kernel found in any partition of {src}: tried {partitions}")
    finally:
        sudo("qemu-nbd", "--disconnect", nbd_dev)
        mnt.rmdir()


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


def _find_kernel(snap_dir: Path | None, disk: Path | None = None) -> tuple[str, str | None]:
    """Return (vmlinuz, initrd_or_None).

    Preference order (for snapshot boots):
      1. Snapshot-cached kernel + initrd
    For provisioning (snap_dir=None):
      2. Guest-extracted kernel + initrd
      3. Host kernel + initrd

    Note: the product kernel (exelet/fs/amd64/kernel/kernel) cannot be used
    for CI VMs because it lacks CONFIG_IFB (needed by the exelet for
    bandwidth limiting) and CONFIG_VFAT_FS (needed for /boot/efi mount).
    """
    if snap_dir is not None:
        snap_vmlinuz = snap_dir / "vmlinuz"
        snap_initrd  = snap_dir / "initrd.img"
        if snap_vmlinuz.exists() and snap_initrd.exists():
            return str(snap_vmlinuz), str(snap_initrd)
        print(f"Warning: kernel not found in {snap_dir}, falling back", flush=True)

    if disk is not None:
        try:
            return _extract_guest_kernel(disk)
        except Exception as e:
            print(f"Warning: could not extract guest kernel: {e}", flush=True)

    uname = os.uname().release
    for vmlinuz in (f"/boot/vmlinuz-{uname}", "/boot/vmlinuz"):
        if Path(vmlinuz).exists():
            break
    else:
        raise RuntimeError("Could not locate vmlinuz")
    for initrd in (f"/boot/initrd.img-{uname}", "/boot/initrd.img"):
        if Path(initrd).exists():
            break
    else:
        raise RuntimeError("Could not locate initrd.img")
    return vmlinuz, initrd


# ── VM launch ──────────────────────────────────────────────────────────────────

def _launch_ch(disk: Path, data_disk: Path, seed: Path,
               tap: str, mac: str, ip: str,
               snap_dir: Path | None) -> int:
    """Start cloud-hypervisor in the background; return its PID."""
    vmlinuz, initrd = _find_kernel(snap_dir, disk=disk if snap_dir is None else None)
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
    if snap_dir is not None:
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
        f"path={seed},readonly=on",
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


# ── Snapshot creation (first-boot path) ───────────────────────────────────────

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
    """Boot a temporary VM, provision it, save the snapshot, destroy the VM."""
    print(f"Building snapshot: {snap_dir.name}", flush=True)
    ssh_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]
    scp_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]

    # ── Boot a fresh VM from the base image ──
    ip  = allocate_ip()
    mac = mac_for_ip(ip)
    tap = tap_name_for(NAME + "-snap")
    disk      = WORKDIR / f"{NAME}-snap.qcow2"
    data_disk = WORKDIR / f"{NAME}-snap-data.raw"
    seed      = WORKDIR / f"{NAME}-snap-seed.iso"

    flat_base = WORKDIR / f"{BASE_IMG.stem}-flat.qcow2"
    if not flat_base.exists():
        print("Flattening base image for cloud-hypervisor ...", flush=True)
        tmp = Path(str(flat_base) + ".converting")
        sudo("qemu-img", "convert", "-f", "qcow2", "-O", "qcow2",
             str(BASE_IMG), str(tmp))
        sudo("mv", str(tmp), str(flat_base))

    sudo("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
         "-b", str(flat_base), str(disk), f"{DISK_GB}G")
    sudo("truncate", "-s", f"{DATA_GB}G", str(data_disk))
    _make_cloud_init_iso(seed, False, ip, mac)
    _setup_tap(tap)

    pid = _launch_ch(disk, data_disk, seed, tap, mac, ip, snap_dir=None)
    try:
        _wait_ssh(ip, timeout=300)
        print("SSH ready (snapshot provisioning VM).", flush=True)

        _provision_vm(ip, ssh_opts, scp_opts)
        _save_snapshot(ip, disk, data_disk, snap_dir, ssh_opts, scp_opts)
    finally:
        # Destroy the temporary VM.
        subprocess.run(["sudo", "kill", str(pid)], capture_output=True)
        for f in [disk, data_disk, seed]:
            subprocess.run(["sudo", "rm", "-f", str(f)], capture_output=True)
        _teardown_tap(tap)


def _provision_vm(ip: str, ssh_opts: list, scp_opts: list) -> None:
    """SSH into a fresh VM, run cloud-init and setup scripts."""
    # Wait for cloud-init. Exit code 2 = "recoverable error" (tolerable).
    # cloud-init v25.3 can return exit 1 while still running; retry up to 60s.
    for attempt in range(12):
        r = subprocess.run(
            ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", "sudo cloud-init status --wait"],
            capture_output=True, text=True,
        )
        if r.returncode in (0, 2):
            break
        combined = r.stdout + r.stderr
        if attempt < 11:
            print(f"cloud-init status exited {r.returncode} (attempt {attempt+1}/12), retrying in 5s...")
            print(combined.strip())
            time.sleep(5)
        else:
            print(combined.strip())
            raise RuntimeError(f"cloud-init status exited {r.returncode} after {attempt+1} attempts")
    if r.returncode == 2:
        subprocess.run(
            ["ssh", *ssh_opts, f"{USER_NAME}@{ip}",
             "sudo cloud-init status --long; sudo cat /var/log/cloud-init-output.log | tail -50"],
        )

    vm_arch_raw = subprocess.check_output(
        ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", "uname -m"], text=True,
    ).strip()
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

    # Copy Docker Hub auth into VM for authenticated pulls (avoids rate limits).
    docker_cfg = Path.home() / ".docker" / "config.json"
    if docker_cfg.exists():
        run("scp", *scp_opts, str(docker_cfg), f"{USER_NAME}@{ip}:/tmp/docker-config.json")
        run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
            "sudo mkdir -p /root/.docker && sudo mv /tmp/docker-config.json /root/.docker/config.json")
    elif os.environ.get("BUILDKITE"):
        raise RuntimeError(
            "~/.docker/config.json not found. Docker Hub pulls will be rate-limited.\n"
            "Fix: sudo -u buildkite-agent docker login\n"
            f"Expected path: {docker_cfg}"
        )

    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "sudo mv ~/setup-cloud-hypervisor.sh ~/setup-exelet.sh /root/ "
        "&& sudo chmod +x /root/setup-cloud-hypervisor.sh /root/setup-exelet.sh")

    print("Running setup-cloud-hypervisor.sh ...", flush=True)
    run("ssh", *ssh_opts, "-o", "LogLevel=ERROR", f"{USER_NAME}@{ip}",
        "sudo /bin/bash -x /root/setup-cloud-hypervisor.sh")

    print("Running setup-exelet.sh ...", flush=True)
    run("ssh", *ssh_opts, "-o", "LogLevel=ERROR", f"{USER_NAME}@{ip}",
        "sudo /bin/bash -x /root/setup-exelet.sh")


def _save_snapshot(ip: str, disk: Path, data_disk: Path, snap_dir: Path,
                   ssh_opts: list, scp_opts: list) -> None:
    """Sync, extract kernel, and save disk images to snap_dir atomically."""
    staging = Path(str(snap_dir) + ".staging")
    sudo("rm", "-rf", str(staging))
    sudo("mkdir", "-p", str(staging))
    sudo("chmod", "777", str(staging))

    # Export ZFS pool cleanly so the data disk is consistent, then sync.
    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "sudo zpool export tank 2>/dev/null || true; sudo sync")

    # Extract guest kernel for future snapshot boots.
    print("Extracting kernel/initrd from VM ...", flush=True)
    uname_r = subprocess.check_output(
        ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", "uname -r"], text=True).strip()
    with tempfile.TemporaryDirectory() as td:
        td_path = Path(td)
        run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
            f"sudo cp /boot/vmlinuz-{uname_r} /boot/initrd.img-{uname_r} /tmp/"
            f" && sudo chmod a+r /tmp/vmlinuz-{uname_r} /tmp/initrd.img-{uname_r}")
        run("scp", *scp_opts,
            f"{USER_NAME}@{ip}:/tmp/vmlinuz-{uname_r}",
            f"{USER_NAME}@{ip}:/tmp/initrd.img-{uname_r}",
            str(td_path) + "/")
        sudo("cp", str(td_path / f"vmlinuz-{uname_r}"),   str(staging / "vmlinuz"))
        sudo("cp", str(td_path / f"initrd.img-{uname_r}"), str(staging / "initrd.img"))
    sudo("chmod", "a+r", str(staging / "vmlinuz"), str(staging / "initrd.img"))
    print(f"Kernel {uname_r} saved to staging", flush=True)

    cp_clone(disk, staging / "base.qcow2")
    cp_clone(data_disk, staging / "data.raw")
    sudo("chmod", "a+r", str(staging / "base.qcow2"), str(staging / "data.raw"))

    # Atomic rename: snap_dir appears fully populated or not at all.
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

    for key in ("VM_DISK", "VM_DATA_DISK", "VM_SEED"):
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

    if not BASE_IMG.exists():
        print("Downloading base image ...", flush=True)
        sudo("curl", "-L", BASE_URL, "-o", str(BASE_IMG))
    sidecar = Path(str(BASE_IMG) + ".sha256")
    if not sidecar.exists():
        result = subprocess.run(["sha256sum", str(BASE_IMG)], capture_output=True, text=True)
        sha = result.stdout.split()[0]
        subprocess.run(["sudo", "tee", str(sidecar)],
                       input=sha + "\n", text=True, capture_output=True)
    b_hash = sidecar.read_text().strip()

    snap_dir, local_base, local_data = _snapshot_paths(s_hash, img_dig, b_hash)
    snap_base = snap_dir / "base.qcow2"
    snap_data = snap_dir / "data.raw"

    # ── Ensure snapshot exists (builds one if needed) ──
    _ensure_snapshot(snap_dir)

    # ── Boot from snapshot ──
    disk      = WORKDIR / f"{NAME}.qcow2"
    data_disk = WORKDIR / f"{NAME}-data.raw"
    seed      = WORKDIR / f"{NAME}-seed.iso"
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

    def make_iso():
        _make_cloud_init_iso(seed, True, ip, mac)

    def setup_tap():
        _setup_tap(tap)

    with ThreadPoolExecutor(max_workers=4) as pool:
        futs = {
            pool.submit(clone_root): "clone_root",
            pool.submit(clone_data): "clone_data",
            pool.submit(make_iso):   "make_iso",
            pool.submit(setup_tap):  "setup_tap",
        }
        for fut in as_completed(futs):
            task = futs[fut]
            exc  = fut.exception()
            if exc:
                raise RuntimeError(f"Parallel task {task!r} failed: {exc}") from exc

    pid = _launch_ch(disk, data_disk, seed, tap, mac, ip, snap_dir)

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
        f"VM_SEED={seed}\n"
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

    if not BASE_IMG.exists():
        print("Downloading base image ...", flush=True)
        sudo("curl", "-L", BASE_URL, "-o", str(BASE_IMG))
    sidecar = Path(str(BASE_IMG) + ".sha256")
    if not sidecar.exists():
        result = subprocess.run(["sha256sum", str(BASE_IMG)], capture_output=True, text=True)
        sha = result.stdout.split()[0]
        subprocess.run(["sudo", "tee", str(sidecar)],
                       input=sha + "\n", text=True, capture_output=True)
    b_hash = sidecar.read_text().strip()

    snap_dir, _, _ = _snapshot_paths(s_hash, img_dig, b_hash)
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
