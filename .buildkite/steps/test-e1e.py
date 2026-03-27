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

    shard = os.environ.get("E1E_SHARD", "")
    suffix = f"-{shard}" if shard else ""
    run_filter = os.environ.get("E1E_RUN_FILTER", "")

    print(f"--- :rocket: Run e1e tests{' (shard ' + shard + ')' if shard else ''} (includes VM startup)", flush=True)
    # Remove stale golden files so they get freshly regenerated.
    # When sharded, only remove files matching this shard's filter so we
    # don't delete golden files that belong to other shards.
    for f in _glob("e1e/golden/*.txt"):
        basename = os.path.basename(f).removesuffix(".txt")
        # Golden files are TestName.txt or TestName_subtest.txt;
        # the top-level test name is everything before the first underscore.
        test_name = basename.split("_")[0]
        if run_filter and not re.match(run_filter, test_name):
            continue
        os.remove(f)

    log_artifact_dir = f"e1e-logs{suffix}"
    os.makedirs(log_artifact_dir, exist_ok=True)
    os.environ["E1E_LOG_DIR"] = os.path.abspath(log_artifact_dir)

    json_results = f"e1e-results{suffix}.json"

    cmd = ["go", "tool", "gotestsum", "--format", "testname", "--jsonfile", json_results,
           "--", "-race", "-timeout=15m", "-failfast"]
    if run_filter:
        cmd.extend(["-run", run_filter])
    cmd.append("./e1e")

    env = {**os.environ, "E1_VM_CONCURRENCY": "12", "GITHUB_ACTIONS": "false"}
    test_result = subprocess.run(cmd, env=env)

    _annotate_results(json_results, shard)

    # Only check golden files if tests passed — if they failed, we deleted
    # the files before the run and they were never regenerated.
    if test_result.returncode == 0:
        print(f"--- :scroll: Check golden files unchanged{' (shard ' + shard + ')' if shard else ''}", flush=True)
        result = subprocess.run(["git", "status", "--porcelain", "e1e/golden/"], capture_output=True, text=True)
        changed_lines = result.stdout.strip().splitlines() if result.stdout.strip() else []
        if run_filter:
            # Only check golden files matching this shard's test filter.
            filtered = []
            for line in changed_lines:
                # git status --porcelain lines look like: " M e1e/golden/TestFoo.txt"
                path = line.split()[-1] if line.split() else ""
                basename = os.path.basename(path).removesuffix(".txt")
                test_name = basename.split("_")[0]
                if re.match(run_filter, test_name):
                    filtered.append(line)
            changed_lines = filtered
        if changed_lines:
            print("ERROR: Golden files were modified by tests:", flush=True)
            for line in changed_lines:
                print(line, flush=True)
            run(["git", "--no-pager", "diff", "e1e/golden/"])

            # Push changes to a recovery branch
            _push_golden_recovery_branch()

            sys.exit(1)

    recording_file = f"recordings{suffix}.html"
    print("--- :film_projector: Generate asciinema recordings", flush=True)
    rec_result = subprocess.run(["go", "run", "./cmd/asciinema-viewer", "e1e", recording_file])
    if rec_result.returncode != 0:
        print("WARNING: asciinema recording generation failed (non-fatal)", flush=True)

    sys.exit(test_result.returncode)


def _restore_prebuilt_artifacts():
    print("--- :package: Restore prebuilt artifacts", flush=True)
    goarch = os.environ.get("GOARCH", "amd64")
    ci_cache = os.environ.get("CI_CACHE", "")
    build_id = os.environ.get("BUILDKITE_BUILD_ID", "local")
    if ci_cache:
        default_prebuilt = f"{ci_cache}/e1e-prebuilt-{build_id}"
    else:
        default_prebuilt = f"/tmp/e1e-prebuilt-{build_id}"
    prebuilt = os.environ.get("PREBUILT_DIR", default_prebuilt)

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


def _push_golden_recovery_branch():
    """Push golden file changes to a recovery branch for easy cherry-picking."""
    build_id = os.environ.get("BUILDKITE_BUILD_ID", "unknown")
    branch = f"golden-{build_id}"
    try:
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        import github_token
        token = github_token.get()
        github_token.configure_origin(token)

        run(["git", "config", "user.name", "exe CI"])
        run(["git", "config", "user.email", "ci@exe.dev"])
        run(["git", "add", "e1e/golden/"])
        run(["git", "commit", "-m", f"Golden file updates from CI build {build_id}"])
        result = subprocess.run(
            ["./bin/retry.sh", "--retry-on", "128", "git", "push", "origin", f"HEAD:refs/heads/{branch}"],
        )
        if result.returncode == 0:
            print(f"\n\U0001f4e6 Golden file changes pushed to branch: {branch}", flush=True)
            print(f"To apply these changes locally:", flush=True)
            print(f"  git fetch origin {branch} && git cherry-pick FETCH_HEAD", flush=True)
        else:
            print(f"WARNING: Failed to push golden recovery branch", flush=True)
    except Exception as e:
        print(f"WARNING: Could not push golden recovery branch: {e}", flush=True)


def _has_cmd(name):
    return subprocess.run(["which", name], capture_output=True).returncode == 0


def _glob(pattern):
    import glob
    return glob.glob(pattern)


if __name__ == "__main__":
    main()
