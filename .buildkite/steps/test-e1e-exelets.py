#!/usr/bin/env python3
"""Run e1e/testinfra and e1e/exelets tests.

These packages have their own TestMain, so they get an independent
exed+exelet instance separate from the main e1e tests.
"""

import json
import os
import re
import subprocess
import sys

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

    _restore_prebuilt_artifacts()
    _destroy_stale_vms()

    print("--- :electric_plug: Run exelets tests (includes VM startup)", flush=True)

    os.makedirs("e1e-logs-exelets", exist_ok=True)
    os.environ["E1E_LOG_DIR"] = os.path.abspath("e1e-logs-exelets")

    json_results = "e1e-results-exelets.json"
    junit_results = "e1e-results-exelets.xml"

    vm_concurrency = os.environ.get("E1E_EXELETS_VM_CONCURRENCY", os.environ.get("E1E_VM_CONCURRENCY", "10"))
    env = {**os.environ, "E1_VM_CONCURRENCY": vm_concurrency, "GITHUB_ACTIONS": "false"}
    test_result = subprocess.run(
        ["go", "tool", "gotestsum", "--format", "testname", "--jsonfile", json_results,
         "--junitfile", junit_results,
         "--", "-race", "-count=1", "-timeout=15m", "-failfast",
         "./e1e/testinfra", "./e1e/exelets"],
        env=env,
    )

    _annotate_results(json_results)
    _generate_gantt(json_results)
    _upload_test_analytics(junit_results)

    sys.exit(test_result.returncode)


def _restore_prebuilt_artifacts():
    print("--- :package: Restore prebuilt artifacts", flush=True)
    goarch = os.environ.get("GOARCH", "amd64")
    ci_cache = os.environ.get("CI_CACHE", os.path.join(os.environ.get("HOME", "/tmp"), ".cache", "ci"))
    build_id = os.environ.get("BUILDKITE_BUILD_ID", "local")
    default_prebuilt = f"{ci_cache}/e1e-prebuilt-{build_id}"
    prebuilt = os.environ.get("PREBUILT_DIR", default_prebuilt)

    # Tell the Go test harness where to find prebuilt binaries.
    for name, binary in [("PREBUILT_EXED", "exed"), ("PREBUILT_EXEPROX", "exeprox"),
                         ("PREBUILT_SSHPIPERD", "sshpiperd"), ("PREBUILT_EXELET", "exeletd")]:
        path = f"{prebuilt}/{binary}"
        if os.path.isfile(path):
            os.environ[name] = path

    fs_cache = f"{prebuilt}/exelet-fs-{goarch}"
    if os.path.isdir(fs_cache):
        fs_dest = f"exelet/fs/{goarch}"
        os.makedirs(fs_dest, exist_ok=True)
        # Use "/. " suffix to copy contents into existing dir (not nest).
        run(["cp", "--reflink=auto", "-a", fs_cache + "/.", fs_dest + "/"])
    else:
        run(["make", "exelet-fs"])

    ui_cache = f"{prebuilt}/ui-dist"
    if os.path.isdir(ui_cache):
        os.makedirs("ui", exist_ok=True)
        run(["cp", "--reflink=auto", "-a", ui_cache, "ui/dist"])
    else:
        run(["make", "ui"])

    init_cache = f"{prebuilt}/exe-init"
    if os.path.isfile(init_cache):
        os.makedirs("exelet/vmm/cloudhypervisor", exist_ok=True)
        run(["cp", "--reflink=auto", init_cache, "exelet/vmm/cloudhypervisor/exe-init"])
    else:
        run(["make", "exe-init"])


def _destroy_stale_vms():
    print("--- :boom: Destroy stale VMs (older than 1 hour)", flush=True)
    cutoff = subprocess.run(
        ["date", "-d", "1 hour ago", "+%Y%m%d%H%M%S"],
        capture_output=True, text=True,
    ).stdout.strip()

    import glob as globmod
    for pidfile in globmod.glob("/tmp/ch-pid-ci-ubuntu-*"):
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


def _generate_gantt(json_results):
    """Generate a per-test gantt chart HTML artifact."""
    if not os.path.isfile(json_results):
        return
    output = "test-gantt-exelets.html"
    result = subprocess.run(
        ["python3", "bin/ci-test-gantt", json_results, output, "e1e exelets"],
    )
    if result.returncode != 0:
        print("WARNING: gantt chart generation failed (non-fatal)", flush=True)


def _annotate_results(json_results):
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
            action, test = ev.get("Action", ""), ev.get("Test", "")
            pkg = ev.get("Package", "").replace("exe.dev/", "")
            elapsed = ev.get("Elapsed", 0.0)
            if test and action in ("pass", "fail", "skip") and "/" not in test:
                tests[f"{pkg}.{test}"] = (elapsed, action)
                if pkg not in pkg_stats:
                    pkg_stats[pkg] = {"pass": 0, "fail": 0, "skip": 0, "elapsed": 0.0}
                pkg_stats[pkg][action] += 1
                if action in ("pass", "fail"):
                    pkg_stats[pkg]["elapsed"] = max(pkg_stats[pkg]["elapsed"], elapsed)

    lines = ["**e1e/exelets timing**\n", "| Test | Duration | Status |", "|------|----------|--------|"]
    for name, (elapsed, action) in sorted(tests.items(), key=lambda x: -x[1][0])[:20]:
        icon = {"pass": "✅", "fail": "❌"}.get(action, "⏭️")
        lines.append(f"| `{name}` | {elapsed:.1f}s | {icon} |")
    lines += ["\n**Package summary**\n", "| Package | Pass | Fail | Skip | Wall time |", "|---------|------|------|------|-----------|"]
    for pkg, s in sorted(pkg_stats.items()):
        lines.append(f"| `{pkg}` | {s['pass']} | {s['fail']} | {s['skip']} | {s['elapsed']:.1f}s |")

    subprocess.run(
        ["buildkite-agent", "annotate", "--context", "e1e-timing-exelets", "--style", "info"],
        input="\n".join(lines), text=True,
    )


def _upload_test_analytics(junit_file):
    """Upload JUnit XML to Buildkite Test Analytics."""
    if os.environ.get("BUILDKITE") != "true":
        return
    if not os.path.isfile(junit_file):
        return
    result = subprocess.run(
        [".buildkite/steps/upload-test-analytics.sh", junit_file],
    )
    if result.returncode != 0:
        print("WARNING: Test analytics upload failed (non-fatal)", flush=True)


def _has_cmd(name):
    return subprocess.run(["which", name], capture_output=True).returncode == 0


if __name__ == "__main__":
    main()
