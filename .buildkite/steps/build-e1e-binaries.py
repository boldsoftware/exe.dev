#!/usr/bin/env python3
"""Pre-build e1e test binaries so downstream jobs skip compilation.

Two independent pipelines run in parallel, joined at the end:

  A (exelet chain):  exelet-fs  →  exe-init  →  exeletd
  B (ui+exed):       make ui    →  go build exed

Plus two standalone Go builds: exeprox, sshpiperd.
Plus a "warmer" that pre-compiles exed's deps to shorten B's final step
(helpful when the UI cache misses and B's first stage takes a while).

Artifacts are placed in ~/.cache/ci/e1e-prebuilt-{BUILD_ID}/ and shared
with downstream shards.  Override the cache root via CI_CACHE env var.
"""

import os
import subprocess
import sys
import threading
import time

def run(args, **kwargs):
    print(f"+ {' '.join(args)}", flush=True)
    subprocess.run(args, check=True, **kwargs)


class Task:
    """A subprocess-based task that records its real elapsed time.

    `start_after` is an optional Task whose completion must precede ours.
    All Task objects should be registered via Task.all so main can join them.
    """

    all: list["Task"] = []

    def __init__(self, name, argv, *, after=(), cwd=None, env=None, optional=False):
        self.name = name
        self.argv = argv
        self.cwd = cwd
        self.env = env
        # Sequence of Tasks that must finish (successfully, unless optional)
        # before this one starts.
        self.after = after if isinstance(after, (list, tuple)) else (after,)
        self.optional = optional  # if True, failure is non-fatal
        self.elapsed = None
        self.returncode = None
        self._thread = threading.Thread(target=self._run, name=name, daemon=True)
        Task.all.append(self)

    def _run(self):
        for dep in self.after:
            dep._thread.join()
            if dep.returncode != 0 and not dep.optional:
                # Upstream failed; skip us.
                self.returncode = -1
                return
        t0 = time.monotonic()
        p = subprocess.Popen(self.argv, cwd=self.cwd, env=self.env)
        self.returncode = p.wait()
        self.elapsed = time.monotonic() - t0

    def start(self):
        self._thread.start()

    def join(self):
        self._thread.join()


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
    # Export CI_CACHE so subprocesses (e.g., make ui) can use persistent cache locations.
    os.environ["CI_CACHE"] = ci_cache
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

    # When E1E_COVERAGE is set, build exed and exeprox with coverage instrumentation.
    coverage = os.environ.get("E1E_COVERAGE", "") == "true"
    cover_flags = ["-cover", "-covermode=atomic", "-coverpkg=exe.dev/..."] if coverage else []
    if coverage:
        print("  coverage mode: building exed+exeprox with -cover", flush=True)

    linux_env = {**os.environ, "GOOS": "linux", "CGO_ENABLED": "0"}

    # Pipeline A: exelet-fs -> exe-init -> exeletd.  exeletd embeds exelet/fs
    # via //go:embed, and exe-init writes into that dir, so these are serial.
    fs_task = Task("exelet-fs", ["bash", "-c", _exelet_fs_script(ci_cache, goarch)])
    init_task = Task("exe-init", ["make", "exe-init"], after=fs_task)
    exeletd_task = Task(
        "exeletd",
        ["go", "build"] + cover_flags + ["-ldflags=-s -w", "-o", f"{out}/exeletd", "./cmd/exelet"],
        after=init_task,
        env=linux_env,
    )

    ui_task = Task("ui", ["make", "ui"])

    # Standalone Go builds (no dependencies).
    exeprox_task = Task(
        "exeprox",
        ["go", "build", "-race", "-ldflags=-s -w"] + cover_flags + ["-o", f"{out}/exeprox", "./cmd/exeprox"],
    )
    sshpiperd_task = Task(
        "sshpiperd",
        ["go", "build", "-race", "-ldflags=-s -w", "-o", f"{out}/sshpiperd", "./cmd/sshpiperd"],
        cwd="deps/sshpiper",
    )

    # Pre-warm the Go race cache for exed's biggest dependencies.  Running
    # this in parallel with exed wastes CPU (they recompile the same deps and
    # fight the lock); instead, warm first, then link exed.  The warmer
    # overlaps with the A pipeline, UI, and the other Go builds.
    warm_task = Task(
        "exed-deps-warm",
        ["go", "build", "-race"] + cover_flags + ["./execore", "./exedb", "./billing", "./llmgateway"],
    )

    # Pipeline B: (ui & warm) -> go build exed.  exed embeds ui/dist via
    # //go:embed, so the UI must be ready before linking.  The warmer fills
    # the Go build cache so the final exed build is just a link.
    exed_task = Task(
        "exed",
        ["go", "build", "-race", "-ldflags=-s -w"] + cover_flags + ["-o", f"{out}/exed", "./cmd/exed"],
        after=(ui_task, warm_task),
    )

    # Kick them all off at once.
    for t in Task.all:
        t.start()

    # Join all.
    for t in Task.all:
        t.join()

    # Report & check.
    failed = []
    for t in Task.all:
        if t.returncode == 0:
            continue
        if t.optional:
            print(f"  warning: {t.name} failed (non-fatal)", flush=True)
            continue
        failed.append(t.name)
    if failed:
        print(f"FAILED tasks: {', '.join(failed)}", file=sys.stderr, flush=True)
        sys.exit(1)

    total = time.monotonic() - t0

    # Copy artifacts for downstream shards.
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

    # Print timing breakdown (real per-task elapsed, not wall-clock since t0).
    print("\nPer-task elapsed (wall clock of each subprocess):", flush=True)
    order = [
        "exelet-fs", "exe-init", "exeletd",
        "ui", "exed",
        "exeprox", "sshpiperd", "exed-deps-warm",
    ]
    by_name = {t.name: t for t in Task.all}
    for name in order:
        t = by_name.get(name)
        if t is not None and t.elapsed is not None:
            print(f"  {name}: {t.elapsed:.1f}s", flush=True)
    print(f"  total wall: {total:.1f}s", flush=True)

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
