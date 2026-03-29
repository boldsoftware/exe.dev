#!/usr/bin/env python3
"""Pre-build e1e test binaries so downstream jobs skip compilation.

Builds everything in parallel where possible:
  - exelet-fs restore, exe-init build, UI build all start immediately
  - Go binary builds (exeprox, sshpiperd) start immediately
  - exeletd waits for exelet-fs + exe-init (it embeds the fs via //go:embed)
  - exed links against ui/dist, so it waits for UI to finish

Artifacts are placed in ~/.cache/ci/e1e-prebuilt-{BUILD_ID}/ and shared
with downstream shards.  Override the cache root via CI_CACHE env var.
"""

import os
import subprocess
import sys
import time

def run(args, **kwargs):
    print(f"+ {' '.join(args)}", flush=True)
    subprocess.run(args, check=True, **kwargs)

def timed(label, fn):
    """Run fn(), print elapsed time."""
    t0 = time.monotonic()
    result = fn()
    dt = time.monotonic() - t0
    print(f"  {label}: {dt:.1f}s", flush=True)
    return result

def main():
    os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("HOME", "") + "/go/bin:" + os.environ.get("HOME", "") + "/.local/bin:" + os.environ["PATH"]

    print("--- :go: Set up Go", flush=True)
    run(["go", "version"])

    print("--- :package: Ensure b2 CLI available", flush=True)
    if not _has_cmd("b2"):
        run(["./bin/retry.sh", "bash", "-c", "set -o pipefail; curl -LsSf https://astral.sh/uv/install.sh | sh"])
        os.environ["PATH"] = os.environ.get("HOME", "") + "/.local/bin:" + os.environ["PATH"]
        run(["./bin/retry.sh", "uv", "tool", "install", "b2"])

    ci_cache = os.environ.get("CI_CACHE", os.path.join(os.environ.get("HOME", "/tmp"), ".cache", "ci"))
    goarch = os.environ.get("GOARCH", "amd64")
    build_id = os.environ.get("BUILDKITE_BUILD_ID", "local")

    # Output dir for prebuilt binaries — on same ZFS filesystem as builds.
    out = f"{ci_cache}/e1e-prebuilt-{build_id}"
    subprocess.run(["rm", "-rf", out])
    os.makedirs(out, exist_ok=True)

    # Clean up prebuilt dirs from previous builds.
    _cleanup_old_prebuilt(ci_cache, build_id)

    print("--- :wrench: Build all artifacts (parallel)", flush=True)
    t0 = time.monotonic()

    # ── Start all tasks concurrently ──

    # 1. Restore exelet-fs (cached tarball or Backblaze download)
    fs_proc = subprocess.Popen(["bash", "-c", _exelet_fs_script(ci_cache, goarch)])

    # 2. Build UI (Node.js + Vite, ~3-6s)
    ui_proc = subprocess.Popen(["bash", "-c", "make ui"])

    # When E1E_COVERAGE is set, build exed and exeprox with coverage instrumentation.
    coverage = os.environ.get("E1E_COVERAGE", "") == "true"
    cover_flags = ["-cover", "-covermode=atomic", "-coverpkg=exe.dev/..."] if coverage else []
    if coverage:
        print("  coverage mode: building exed+exeprox with -cover", flush=True)

    # 4. Go binaries that don't depend on UI or exelet-fs — start immediately
    go_procs = {}
    go_procs["exeprox"] = subprocess.Popen(
        ["go", "build", "-race"] + cover_flags + ["-o", f"{out}/exeprox", "./cmd/exeprox"])
    go_procs["sshpiperd"] = subprocess.Popen(
        ["go", "build", "-race", "-o", f"{out}/sshpiperd", "./cmd/sshpiperd"],
        cwd="deps/sshpiper")
    # Note: exeletd is built later — it embeds exelet/fs via //go:embed,
    # so exelet-fs + exe-init must complete first.

    # ── Wait for tasks, tracking timing ──
    timings = {}
    failed = False

    # Wait for non-exed Go builds
    for name, proc in go_procs.items():
        t_start = t0  # they all started at t0
        if proc.wait() != 0:
            print(f"  FAILED: {name}", file=sys.stderr, flush=True)
            failed = True
        else:
            timings[name] = time.monotonic() - t0

    # Wait for exelet-fs (must finish before exe-init, which writes into fs dir)
    if fs_proc.wait() != 0:
        print("  FAILED: exelet-fs", file=sys.stderr, flush=True)
        failed = True
    else:
        timings["exelet-fs"] = time.monotonic() - t0

    # Build exe-init after exelet-fs so they don't race on the fs directory.
    if not failed:
        t_init = time.monotonic()
        init_result = subprocess.run(["make", "exe-init"])
        if init_result.returncode != 0:
            print("  FAILED: exe-init", file=sys.stderr, flush=True)
            failed = True
        else:
            timings["exe-init"] = time.monotonic() - t0

    # Build exeletd after exelet-fs + exe-init (it embeds the fs directory).
    # In coverage mode, build with -cover so downstream tests get exelet coverage data.
    if not failed:
        t_exeletd = time.monotonic()
        exeletd_cmd = ["go", "build"] + cover_flags + ["-ldflags=-s -w", "-o", f"{out}/exeletd", "./cmd/exelet"]
        exeletd_result = subprocess.run(
            exeletd_cmd,
            env={**os.environ, "GOOS": "linux", "CGO_ENABLED": "0"})
        if exeletd_result.returncode != 0:
            print("  FAILED: exeletd", file=sys.stderr, flush=True)
            failed = True
        else:
            timings["exeletd"] = time.monotonic() - t0

    # Wait for UI — exed needs ui/dist
    if ui_proc.wait() != 0:
        print("  FAILED: ui", file=sys.stderr, flush=True)
        failed = True
    else:
        timings["ui"] = time.monotonic() - t0

    if failed:
        print("One or more parallel tasks failed", file=sys.stderr)
        sys.exit(1)

    # 5. Build exed (depends on ui/dist being present)
    t_exed = time.monotonic()
    exed_result = subprocess.run(
        ["go", "build", "-race"] + cover_flags + ["-o", f"{out}/exed", "./cmd/exed"])
    if exed_result.returncode != 0:
        print("  FAILED: exed", file=sys.stderr, flush=True)
        sys.exit(1)
    timings["exed"] = time.monotonic() - t0
    timings["exed (link)"] = time.monotonic() - t_exed

    total = time.monotonic() - t0

    # ── Copy artifacts for downstream shards ──
    print("--- :package: Cache build artifacts for shards", flush=True)
    run(["cp", "--reflink=auto", "-a", "ui/dist", f"{out}/ui-dist"])
    fs_dir = f"exelet/fs/{goarch}"
    if os.path.isdir(fs_dir):
        run(["cp", "--reflink=auto", "-a", fs_dir, f"{out}/exelet-fs-{goarch}"])
    init_path = f"exelet/fs/{goarch}/rovol/bin/exe-init"
    if os.path.isfile(init_path):
        run(["cp", "--reflink=auto", init_path, f"{out}/exe-init"])

    print(f"--- :white_check_mark: Prebuilt binaries ready ({total:.1f}s)", flush=True)
    run(["ls", "-lh", out])

    # Print timing breakdown
    print("\nBuild timing breakdown:", flush=True)
    for name in ["exelet-fs", "exe-init", "ui", "exeprox", "sshpiperd", "exeletd", "exed", "exed (link)"]:
        if name in timings:
            marker = "*" if name == "exed (link)" else " "
            print(f" {marker} {name}: {timings[name]:.1f}s", flush=True)
    print(f"  total: {total:.1f}s", flush=True)

    # Write the output dir path so pipeline YAML can reference it.
    print(f"PREBUILT_DIR={out}")


def _exelet_fs_script(ci_cache, goarch):
    """Shell script to restore exelet-fs, using CI_CACHE tarball if available."""
    return f"""
set -e
CURRENT_HASH=$(git rev-parse HEAD:exelet/kernel)$(git rev-parse HEAD:exelet/rovol)
CACHE_TAR="{ci_cache}/exelet-fs-{goarch}-$CURRENT_HASH.tar.gz"
FS_DIR="exelet/fs/{goarch}"

if [ -f "$CACHE_TAR" ]; then
    echo "Restoring exelet-fs from cache ($CURRENT_HASH)..."
    rm -rf "$FS_DIR"/*
    mkdir -p "$FS_DIR"
    tar zxf "$CACHE_TAR" -C "$FS_DIR" --exclude='._*'
    echo "$CURRENT_HASH" > exelet/fs/.hash-{goarch}
    echo "✓ exelet-fs restored from cache"
else
    make exelet-fs
    # Cache the tarball for future builds. Exclude exe-init and exe-ssh which
    # are rebuilt from source each time (and may be written by the parallel
    # exe-init build racing with this tar).
    if [ -d "$FS_DIR/kernel" ] && [ ! -f "$CACHE_TAR" ]; then
        echo "Caching exelet-fs tarball..."
        tar czf "$CACHE_TAR.tmp" -C "$FS_DIR" --exclude='rovol/bin/exe-init' --exclude='rovol/bin/exe-ssh' .
        mv "$CACHE_TAR.tmp" "$CACHE_TAR"
        echo "✓ exelet-fs cached for future builds"
    fi
fi
"""


def _cleanup_old_prebuilt(ci_cache, current_build_id):
    """Remove prebuilt dirs older than 1 hour.

    Only age-based cleanup: concurrent builds may still be using their
    prebuilt dirs for running test shards.
    """
    import glob
    pattern = f"{ci_cache}/e1e-prebuilt-*"
    cutoff = time.time() - 3600  # 1 hour ago
    for d in glob.glob(pattern):
        if current_build_id in d:
            continue
        try:
            mtime = os.path.getmtime(d)
        except OSError:
            continue
        if mtime < cutoff:
            subprocess.run(["rm", "-rf", d])


def _has_cmd(name):
    return subprocess.run(["which", name], capture_output=True).returncode == 0


if __name__ == "__main__":
    main()
