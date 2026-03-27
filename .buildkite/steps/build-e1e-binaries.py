#!/usr/bin/env python3
"""Pre-build e1e test binaries so downstream jobs skip compilation.

Binaries are placed in /tmp/e1e-prebuilt-{BUILDKITE_BUILD_ID}/ and
referenced via PREBUILT_EXED, PREBUILT_EXEPROX, etc.
"""

import os
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

    print("--- :floppy_disk: Download exelet-fs", flush=True)
    run(["make", "exelet-fs"])

    print("--- :hammer: Build exe-init", flush=True)
    run(["make", "exe-init"])

    print("--- :art: Build dashboard UI", flush=True)
    run(["make", "ui"])

    # Scope the output directory to the build so parallel builds don't collide.
    build_id = os.environ.get("BUILDKITE_BUILD_ID", "local")
    out = f"/tmp/e1e-prebuilt-{build_id}"
    subprocess.run(["rm", "-rf", out])
    os.makedirs(out, exist_ok=True)

    print("--- :wrench: Build e1e binaries (parallel)", flush=True)
    procs = []
    procs.append(subprocess.Popen(["go", "build", "-race", "-o", f"{out}/exed", "./cmd/exed"]))
    procs.append(subprocess.Popen(["go", "build", "-race", "-o", f"{out}/exeprox", "./cmd/exeprox"]))
    procs.append(subprocess.Popen(["go", "build", "-race", "-o", f"{out}/sshpiperd", "./cmd/sshpiperd"], cwd="deps/sshpiper"))
    procs.append(subprocess.Popen(["go", "build", "-o", f"{out}/exeletd", "./cmd/exelet"],
                                  env={**os.environ, "GOOS": "linux", "CGO_ENABLED": "0"}))

    failed = False
    for p in procs:
        if p.wait() != 0:
            failed = True
    if failed:
        print("One or more builds failed", file=sys.stderr)
        sys.exit(1)

    print("--- :package: Cache build artifacts for shards", flush=True)
    goarch = os.environ.get("GOARCH", "amd64")
    run(["cp", "-a", "ui/dist", f"{out}/ui-dist"])
    fs_dir = f"exelet/fs/{goarch}"
    if os.path.isdir(fs_dir):
        run(["cp", "-a", fs_dir, f"{out}/exelet-fs-{goarch}"])
    init_path = "exelet/vmm/cloudhypervisor/exe-init"
    if os.path.isfile(init_path):
        run(["cp", init_path, f"{out}/exe-init"])

    print("--- :white_check_mark: Prebuilt binaries ready", flush=True)
    run(["ls", "-lh", out])

    # Write the output dir path so pipeline YAML can reference it.
    print(f"PREBUILT_DIR={out}")

def _has_cmd(name):
    return subprocess.run(["command", "-v", name], shell=True,
                          capture_output=True).returncode == 0

if __name__ == "__main__":
    main()
