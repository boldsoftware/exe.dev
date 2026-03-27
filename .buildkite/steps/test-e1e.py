#!/usr/bin/env python3
"""Run e1e end-to-end tests.

Handles prebuilt artifact restoration, stale VM cleanup, test execution,
golden file verification, and asciinema recording generation.
"""

import json
import os
import re
import subprocess
import sys
import tempfile

def run(args, **kwargs):
    print(f"+ {' '.join(args)}", flush=True)
    subprocess.run(args, check=True, **kwargs)

def main():
    os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("HOME", "") + "/go/bin:" + os.environ.get("HOME", "") + "/.local/bin:" + os.environ["PATH"]

    print("--- :go: Set up Go", flush=True)
    run(["go", "version"])

    print("--- :package: Ensure b2 CLI available", flush=True)
    if not _has_cmd("b2"):
        run(["./bin/retry.sh", "bash", "-c", "set -o pipefail; curl -LsSf https://astral.sh/uv/install.sh | sh"])
        os.environ["PATH"] = os.environ.get("HOME", "") + "/.local/bin:" + os.environ["PATH"]
        run(["./bin/retry.sh", "uv", "tool", "install", "b2"])
    run(["b2", "version"])

    _restore_prebuilt_artifacts()
    _destroy_stale_vms()

    vm_driver = os.environ.get("VM_DRIVER", "")
    if vm_driver == "cloudhypervisor":
        print("--- :zap: Using cloud-hypervisor (snapshot handled by ci-vm.py)", flush=True)
    else:
        print("--- :camera: Ensure VM snapshot exists", flush=True)
        run(["./ops/ci-vm-snapshot.sh"])

    print("--- :rocket: Run e1e tests", flush=True)
    # Remove stale golden files so they get freshly regenerated.
    for f in _glob("e1e/golden/*.txt"):
        os.remove(f)

    log_dir = tempfile.mkdtemp()
    os.environ["E1E_LOG_DIR"] = log_dir
    os.makedirs("e1e-logs", exist_ok=True)
    os.symlink(log_dir, "e1e-logs/current") if not os.path.exists("e1e-logs/current") else None

    json_results = tempfile.mktemp(suffix=".json")

    env = {**os.environ, "E1_VM_CONCURRENCY": "10", "GITHUB_ACTIONS": "false"}
    test_result = subprocess.run(
        ["go", "tool", "gotestsum", "--format", "testname", "--jsonfile", json_results,
         "--", "-race", "-timeout=15m", "-failfast", "./e1e"],
        env=env,
    )

    _annotate_results(json_results, "")

    if os.path.exists(json_results):
        os.remove(json_results)

    print("--- :scroll: Check golden files unchanged", flush=True)
    result = subprocess.run(["git", "status", "--porcelain", "e1e/golden/"], capture_output=True, text=True)
    if result.stdout.strip():
        print("ERROR: Golden files were modified by tests:", flush=True)
        run(["git", "status", "--porcelain", "e1e/golden/"])
        run(["git", "diff", "e1e/golden/"])
        sys.exit(1)

    print("--- :film_projector: Generate asciinema recordings", flush=True)
    run(["go", "run", "./cmd/asciinema-viewer", "e1e", "recordings.html"])

    sys.exit(test_result.returncode)


def _restore_prebuilt_artifacts():
    print("--- :package: Restore prebuilt artifacts", flush=True)
    goarch = os.environ.get("GOARCH", "amd64")
    prebuilt = os.environ.get("PREBUILT_DIR", "/tmp/e1e-prebuilt-" + os.environ.get("BUILDKITE_BUILD_ID", "local"))

    # Tell the Go test harness where to find prebuilt binaries.
    for name, binary in [("PREBUILT_EXED", "exed"), ("PREBUILT_EXEPROX", "exeprox"),
                         ("PREBUILT_SSHPIPERD", "sshpiperd"), ("PREBUILT_EXELET", "exeletd")]:
        path = f"{prebuilt}/{binary}"
        if os.path.isfile(path):
            os.environ[name] = path

    fs_cache = f"{prebuilt}/exelet-fs-{goarch}"
    if os.path.isdir(fs_cache):
        os.makedirs("exelet/fs", exist_ok=True)
        run(["cp", "-a", fs_cache, f"exelet/fs/{goarch}"])
    else:
        run(["make", "exelet-fs"])

    ui_cache = f"{prebuilt}/ui-dist"
    if os.path.isdir(ui_cache):
        os.makedirs("ui", exist_ok=True)
        run(["cp", "-a", ui_cache, "ui/dist"])
    else:
        run(["make", "ui"])

    init_cache = f"{prebuilt}/exe-init"
    if os.path.isfile(init_cache):
        os.makedirs("exelet/vmm/cloudhypervisor", exist_ok=True)
        run(["cp", init_cache, "exelet/vmm/cloudhypervisor/exe-init"])
    else:
        run(["make", "exe-init"])


def _destroy_stale_vms():
    print("--- :boom: Destroy stale VMs (older than 1 hour)", flush=True)
    cutoff = subprocess.run(
        ["date", "-d", "1 hour ago", "+%Y%m%d%H%M%S"],
        capture_output=True, text=True,
    ).stdout.strip()

    # Clean up stale libvirt VMs.
    result = subprocess.run(["sudo", "virsh", "list", "--name"], capture_output=True, text=True)
    if result.returncode == 0:
        for vm in result.stdout.strip().splitlines():
            vm = vm.strip()
            if not vm.startswith("ci-ubuntu-"):
                continue
            m = re.search(r"(\d{14})$", vm)
            ts = m.group(1) if m else ""
            if not ts or ts < cutoff:
                print(f"  destroying stale VM: {vm}", flush=True)
                subprocess.run(["sudo", "virsh", "destroy", vm], capture_output=True)

    # Clean up stale cloud-hypervisor VMs.
    import glob
    for pidfile in glob.glob("/tmp/ch-pid-ci-ubuntu-*"):
        m = re.search(r"(\d{14})", pidfile)
        if not m:
            continue
        ts = m.group(1)
        if ts >= cutoff:
            continue
        try:
            pid = open(pidfile).read().strip()
        except OSError:
            continue
        if pid:
            print(f"  killing stale CH VM (PID {pid})", flush=True)
            subprocess.run(["sudo", "kill", pid], capture_output=True)
        subprocess.run(["sudo", "rm", "-f", pidfile])


def _annotate_results(json_results, shard):
    """Post a Buildkite annotation with test timing."""
    if os.environ.get("BUILDKITE") != "true":
        return
    if not os.path.isfile(json_results):
        return

    tests = {}
    pkg_stats = {}
    with open(json_results) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            ev = json.loads(line)
            action = ev.get("Action", "")
            test = ev.get("Test", "")
            pkg = ev.get("Package", "").replace("exe.dev/", "")
            elapsed = ev.get("Elapsed", 0.0)
            if test and action in ("pass", "fail", "skip") and "/" not in test:
                tests[f"{pkg}.{test}"] = (elapsed, action)
                if pkg not in pkg_stats:
                    pkg_stats[pkg] = {"pass": 0, "fail": 0, "skip": 0, "elapsed": 0.0}
                pkg_stats[pkg][action] += 1
                if action in ("pass", "fail"):
                    pkg_stats[pkg]["elapsed"] = max(pkg_stats[pkg]["elapsed"], elapsed)

    label = f"shard {shard}" if shard else "all tests"
    lines = [f"**e1e timing ({label})** — top 20 slowest\n"]
    lines.append("| Test | Duration | Status |")
    lines.append("|------|----------|--------|")
    for name, (elapsed, action) in sorted(tests.items(), key=lambda x: -x[1][0])[:20]:
        icon = {"pass": "✅", "fail": "❌"}.get(action, "⏭️")
        lines.append(f"| `{name}` | {elapsed:.1f}s | {icon} |")
    lines.append("\n**Package summary**\n")
    lines.append("| Package | Pass | Fail | Skip | Wall time |")
    lines.append("|---------|------|------|------|-----------|")
    for pkg, s in sorted(pkg_stats.items()):
        lines.append(f"| `{pkg}` | {s['pass']} | {s['fail']} | {s['skip']} | {s['elapsed']:.1f}s |")

    annotation = "\n".join(lines)
    context = f"e1e-timing{'-' + shard if shard else ''}"
    subprocess.run(
        ["buildkite-agent", "annotate", "--context", context, "--style", "info"],
        input=annotation, text=True,
    )


def _has_cmd(name):
    return subprocess.run(["which", name], capture_output=True).returncode == 0


def _glob(pattern):
    import glob
    return glob.glob(pattern)


if __name__ == "__main__":
    main()
