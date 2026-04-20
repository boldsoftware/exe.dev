#!/usr/bin/env python3
"""Run e1e/billing tests with httprr Stripe cassette recording.

This package has its own TestMain that starts exed with billing enabled
(SkipBilling=false), using an httprr proxy for Stripe API record/replay.

The test always runs in record mode (-httprecord) so the cassette is
freshly generated from real Stripe API calls. If the cassette differs
from what's committed, changes are pushed to a recovery branch.
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

    print("--- :credit_card: Run e1e billing tests (includes VM startup)", flush=True)

    logs_dir = "e1e-logs-billing"
    os.makedirs(logs_dir, exist_ok=True)
    os.environ["E1E_LOG_DIR"] = os.path.abspath(logs_dir)

    json_results = "e1e-results-billing.json"

    vm_concurrency = os.environ.get("E1E_VM_CONCURRENCY", "10")
    env = {**os.environ, "E1_VM_CONCURRENCY": vm_concurrency, "GITHUB_ACTIONS": "false"}
    cmd = ["go", "tool", "gotestsum", "--format", "testname", "--jsonfile", json_results,
           "--", "-race", "-count=1", "-timeout=10m", "-failfast",
           "./e1e/billing",
           "-args", "-httprecord=stripe-checkout"]
    test_result = subprocess.run(cmd, env=env)

    _generate_gantt(json_results)

    # Check cassette changes only if tests passed.
    if test_result.returncode == 0:
        _check_cassettes()

    sys.exit(test_result.returncode)


def _check_cassettes():
    """Check if httprr cassettes changed and push a recovery branch if so."""
    print("--- :floppy_disk: Check httprr cassettes unchanged", flush=True)
    result = subprocess.run(
        ["git", "status", "--porcelain", "e1e/billing/testdata/"],
        capture_output=True, text=True,
    )
    if not result.stdout.strip():
        print("Cassettes unchanged.", flush=True)
        return

    print("Stripe httprr cassettes were modified by tests:", flush=True)
    print(result.stdout, flush=True)

    build_id = os.environ.get("BUILDKITE_BUILD_ID", "unknown")
    branch = f"cassette-{build_id}"
    try:
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        import github_token
        token = github_token.get()
        github_token.configure_origin(token)

        run(["git", "config", "user.name", "exe CI"])
        run(["git", "config", "user.email", "ci@exe.dev"])
        run(["git", "add", "e1e/billing/testdata/"])
        run(["git", "commit", "-m", f"billing: update httprr cassettes from CI build {build_id}"])
        push_result = subprocess.run(
            ["./bin/retry.sh", "--retry-on", "128", "git", "push", "origin", f"HEAD:refs/heads/{branch}"],
        )
        if push_result.returncode == 0:
            print(f"\n\U0001f4e6 Cassette changes pushed to branch: {branch}", flush=True)
            print("To apply these changes locally:", flush=True)
            print(f"  git fetch origin {branch} && git cherry-pick FETCH_HEAD", flush=True)
        else:
            print("WARNING: Failed to push cassette recovery branch", flush=True)
    except Exception as e:
        print(f"WARNING: Could not push cassette recovery branch: {e}", flush=True)

    sys.exit(1)


def _restore_prebuilt_artifacts():
    print("--- :package: Restore prebuilt artifacts", flush=True)
    goarch = os.environ.get("GOARCH", "amd64")
    ci_cache = os.environ.get("CI_CACHE", os.path.join(os.environ.get("HOME", "/tmp"), ".cache", "ci"))
    build_id = os.environ.get("BUILDKITE_BUILD_ID", "local")
    default_prebuilt = f"{ci_cache}/e1e-prebuilt-{build_id}"
    prebuilt = os.environ.get("PREBUILT_DIR", default_prebuilt)

    for name, binary in [("PREBUILT_EXED", "exed"), ("PREBUILT_EXEPROX", "exeprox"),
                         ("PREBUILT_SSHPIPERD", "sshpiperd"), ("PREBUILT_EXELET", "exeletd")]:
        path = f"{prebuilt}/{binary}"
        if os.path.isfile(path):
            os.environ[name] = path

    fs_cache = f"{prebuilt}/exelet-fs-{goarch}"
    if os.path.isdir(fs_cache):
        fs_dest = f"exelet/fs/{goarch}"
        os.makedirs(fs_dest, exist_ok=True)
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
        init_dest = f"exelet/fs/{goarch}/rovol/bin/exe-init"
        os.makedirs(os.path.dirname(init_dest), exist_ok=True)
        run(["cp", "--reflink=auto", init_cache, init_dest])
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
    if not os.path.isfile(json_results):
        return
    output = "test-gantt-billing.html"
    result = subprocess.run(
        ["python3", "bin/ci-test-gantt", json_results, output, "e1e billing"],
    )
    if result.returncode != 0:
        print("WARNING: gantt chart generation failed (non-fatal)", flush=True)


def _has_cmd(name):
    return subprocess.run(["which", name], capture_output=True).returncode == 0


if __name__ == "__main__":
    main()
