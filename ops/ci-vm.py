#!/usr/bin/env python3
"""
ci-vm.py  --  CI VM lifecycle management using cloud-hypervisor.

Subcommands:
  create              Create a VM, write envfile, exit.
  destroy ENVFILE     Destroy a VM described by ENVFILE.
  run                 Create a VM, block until SIGTERM/SIGINT, then destroy.

Environment variables (same interface as ci-vm-start.sh):
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
DISK_GB    = int(os.environ.get("DISK_GB",       "40"))
DATA_GB    = int(os.environ.get("DATA_DISK_GB",  "50"))
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
    for args in [
        ["cp", "--reflink=always", "-a", str(src), str(dst)],
        ["cp", "--reflink=auto",   "-a", str(src), str(dst)],
        ["cp",                     "-a", str(src), str(dst)],
    ]:
        r = subprocess.run(args, capture_output=True)
        if r.returncode == 0:
            return
    for args in [
        ["sudo", "cp", "--reflink=always", "-a", str(src), str(dst)],
        ["sudo", "cp",                     "-a", str(src), str(dst)],
    ]:
        r = subprocess.run(args, capture_output=True)
        if r.returncode == 0:
            return
    raise RuntimeError(f"cp_clone failed: {src} -> {dst}")


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
    local_data = WORKDIR  / f"ci-data-{s_hash[:12]}-{img_dig[:12]}-{b_hash[:12]}.qcow2"
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


def _find_kernel(snap_dir: Path | None, disk: Path | None = None) -> tuple[str, str]:
    """Return (vmlinuz, initrd) preferring snap_dir kernel, then guest, then host."""
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
    cmdline = (
        "console=ttyS0 root=LABEL=cloudimg-rootfs rw"
        " systemd.mask=multipathd.service systemd.mask=multipathd.socket"
    )
    if snap_dir is not None:
        cmdline += (
            f" ip={ip}::{gateway}:255.255.255.0:{NAME}:ens4:off"
            " systemd.mask=cloud-init.service"
            " systemd.mask=cloud-config.service"
            " systemd.mask=cloud-final.service"
            " systemd.mask=systemd-sysctl.service"
            " systemd.mask=systemd-udev-settle.service"
            " systemd.mask=zfs-share.service"
        )

    ch_args = [
        CH_BIN,
        "--kernel",    vmlinuz,
        "--initramfs", initrd,
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
        time.sleep(1)

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

def _provision_and_snapshot(ip: str, disk: Path, data_disk: Path,
                             snap_dir: Path, local_base: Path,
                             local_data: Path) -> None:
    """SSH in, run setup scripts, cache the snapshot."""
    ssh_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]
    scp_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]

    # Wait for cloud-init. Exit code 2 = "recoverable error" (tolerable).
    r = subprocess.run(
        ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", "sudo cloud-init status --wait"],
    )
    if r.returncode not in (0, 2):
        raise RuntimeError(f"cloud-init status exited {r.returncode}")
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

    run("ssh", *ssh_opts, f"{USER_NAME}@{ip}",
        "sudo mv ~/setup-cloud-hypervisor.sh ~/setup-exelet.sh /root/ "
        "&& sudo chmod +x /root/setup-cloud-hypervisor.sh /root/setup-exelet.sh")

    print("Running setup-cloud-hypervisor.sh ...", flush=True)
    run("ssh", *ssh_opts, "-o", "LogLevel=ERROR", f"{USER_NAME}@{ip}",
        "sudo /bin/bash -x /root/setup-cloud-hypervisor.sh")

    print("Running setup-exelet.sh ...", flush=True)
    run("ssh", *ssh_opts, "-o", "LogLevel=ERROR", f"{USER_NAME}@{ip}",
        "sudo /bin/bash -x /root/setup-exelet.sh")

    # Save snapshot.
    snap_base = snap_dir / "base.qcow2"
    snap_data = snap_dir / "data.qcow2"
    sudo("mkdir", "-p", str(snap_dir))
    sudo("chmod", "777", str(snap_dir))

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
        sudo("cp", str(td_path / f"vmlinuz-{uname_r}"),   str(snap_dir / "vmlinuz"))
        sudo("cp", str(td_path / f"initrd.img-{uname_r}"), str(snap_dir / "initrd.img"))
    sudo("chmod", "a+r", str(snap_dir / "vmlinuz"), str(snap_dir / "initrd.img"))
    print(f"Kernel {uname_r} saved to {snap_dir}", flush=True)

    cp_clone(disk, snap_base)
    cp_clone(data_disk, snap_data)
    sudo("chmod", "a+r", str(snap_base), str(snap_data))
    cp_clone(snap_base, local_base)
    cp_clone(snap_data, local_data)

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
    print(f"ci-vm.py create  NAME={NAME}", flush=True)
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
    snap_data = snap_dir / "data.qcow2"
    snapshot  = snap_base.exists() and snap_data.exists()

    disk      = WORKDIR / f"{NAME}.qcow2"
    data_disk = WORKDIR / f"{NAME}-data.qcow2"
    seed      = WORKDIR / f"{NAME}-seed.iso"
    tap       = tap_name_for(NAME)

    ip  = allocate_ip()
    mac = mac_for_ip(ip)
    print(f"Allocated IP: {ip}  MAC: {mac}", flush=True)

    # ── Prepare backing images ──
    if snapshot:
        print(f"Snapshot found: {snap_dir}", flush=True)
        backing      = local_base
        data_backing = local_data
    else:
        backing      = BASE_IMG
        data_backing = None

    def _needs_flatten(img: Path) -> bool:
        r = subprocess.run(
            ["qemu-img", "info", "--output=json", str(img)],
            capture_output=True, text=True)
        if r.returncode != 0:
            return True
        info = json.loads(r.stdout)
        return "backing-filename" in info

    def clone_root():
        if snapshot and (not local_base.exists() or _needs_flatten(local_base)):
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
        if not snapshot:
            flat_base = WORKDIR / f"{BASE_IMG.stem}-flat.qcow2"
            if not flat_base.exists():
                print("Flattening base image for cloud-hypervisor ...", flush=True)
                tmp = Path(str(flat_base) + ".converting")
                sudo("qemu-img", "convert", "-f", "qcow2", "-O", "qcow2",
                     str(BASE_IMG), str(tmp))
                sudo("mv", str(tmp), str(flat_base))
            backing_for_disk = flat_base
        else:
            backing_for_disk = backing
        if snapshot:
            sudo("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
                 "-b", str(backing), str(disk))
        else:
            sudo("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
                 "-b", str(backing_for_disk), str(disk), f"{DISK_GB}G")

    def clone_data():
        if snapshot and not local_data.exists():
            cp_clone(snap_data, local_data)
        if data_backing:
            sudo("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
                 "-b", str(data_backing), str(data_disk))
        else:
            sudo("qemu-img", "create", "-f", "qcow2", str(data_disk), f"{DATA_GB}G")

    def make_iso():
        _make_cloud_init_iso(seed, snapshot, ip, mac)

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

    pid = _launch_ch(disk, data_disk, seed, tap, mac, ip,
                     snap_dir if snapshot else None)

    ssh_timeout = 120 if snapshot else 300
    print(f"Waiting for SSH at {ip} (timeout={ssh_timeout}s) ...", flush=True)
    _wait_ssh(ip, timeout=ssh_timeout)
    print("SSH ready.", flush=True)

    # Post-boot setup for snapshot boots.
    if snapshot:
        ssh_opts = ["-o", "StrictHostKeyChecking=no",
                    "-o", "UserKnownHostsFile=/dev/null",
                    "-o", "LogLevel=ERROR"]
        dns_server = f"{BRIDGE_PFX}.1"
        setup_cmds = (
            "sudo zpool import -f -N tank 2>/dev/null || true; "
            "HP=$(awk '/MemTotal/{print int($2/4096)}' /proc/meminfo) && "
            "echo $HP | sudo tee /proc/sys/vm/nr_hugepages >/dev/null; "
            f"sudo mkdir -p /etc/systemd/resolved.conf.d && "
            f"echo -e '[Resolve]\\nDNS={dns_server}' | "
            f"sudo tee /etc/systemd/resolved.conf.d/ci.conf >/dev/null && "
            f"sudo systemctl restart systemd-resolved"
        )
        subprocess.run(
            ["ssh", *ssh_opts, f"{USER_NAME}@{ip}", setup_cmds],
            check=False, capture_output=True, timeout=10)

    # First-boot provisioning + snapshot creation.
    if not snapshot:
        _provision_and_snapshot(
            ip, disk, data_disk, snap_dir, local_base, local_data)

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


def main():
    if len(sys.argv) < 2:
        sys.exit("usage: ci-vm.py {create|destroy|run} [args]")

    cmd = sys.argv[1]
    if cmd == "create":
        cmd_create()
    elif cmd == "destroy":
        cmd_destroy()
    elif cmd == "run":
        cmd_run()
    else:
        sys.exit(f"unknown subcommand: {cmd}")


if __name__ == "__main__":
    main()
