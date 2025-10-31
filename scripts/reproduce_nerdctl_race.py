#!/usr/bin/env python3
"""
Script to reproduce nerdctl CNI networking race conditions.

This script runs multiple nerdctl commands in parallel to trigger
networking-related race conditions, particularly with CNI plugin operations
for creating and deleting network interfaces.
"""

import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime


def run_nerdctl_container(container_num, ssh_host="ubuntu@192.168.122.10"):
    """
    Run a single nerdctl container creation, wait briefly, then cleanup.

    Returns: (container_num, success, error_message)
    """
    container_name = f"test-race-{container_num}-{int(time.time())}"

    # Use SSH multiplexing to avoid connection overhead
    ssh_opts = [
        "-o", "ControlMaster=auto",
        "-o", "ControlPath=~/.ssh/cm-%r@%h:%p",
        "-o", "ControlPersist=600"
    ]

    # Use similar arguments to what the tests use
    run_cmd = [
        "ssh"
    ] + ssh_opts + [
        ssh_host,
        "sudo", "nerdctl",
        "--namespace", "exe",
        "--cgroup-manager", "cgroupfs",
        "run", "-d",
        "--name", container_name,
        "--network", "bridge",
        "--restart", "no",
        "alpine:latest",
        "sleep", "10"
    ]

    try:
        # Run the container
        print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: Starting...")
        result = subprocess.run(
            run_cmd,
            capture_output=True,
            text=True,
            timeout=30
        )

        if result.returncode != 0:
            error_msg = f"FAILED to create: {result.stderr}"
            print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: {error_msg}")
            return (container_num, False, error_msg)

        container_id = result.stdout.strip().split('\n')[-1][:12]
        print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: Created {container_id}")

        # Wait a tiny bit
        time.sleep(0.1)

        # Stop the container
        stop_cmd = [
            "ssh"
        ] + ssh_opts + [
            ssh_host,
            "sudo", "nerdctl",
            "--namespace", "exe",
            "stop", container_name
        ]
        stop_result = subprocess.run(
            stop_cmd,
            capture_output=True,
            text=True,
            timeout=15
        )

        if stop_result.returncode != 0:
            error_msg = f"FAILED to stop: {stop_result.stderr}"
            print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: {error_msg}")
            # Continue to rm anyway
        else:
            print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: Stopped")

        # Remove the container
        rm_cmd = [
            "ssh"
        ] + ssh_opts + [
            ssh_host,
            "sudo", "nerdctl",
            "--namespace", "exe",
            "rm", "-f", container_name
        ]
        rm_result = subprocess.run(
            rm_cmd,
            capture_output=True,
            text=True,
            timeout=15
        )

        if rm_result.returncode != 0:
            error_msg = f"FAILED to remove: {rm_result.stderr}"
            print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: {error_msg}")
            return (container_num, False, error_msg)

        print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: Removed - SUCCESS")
        return (container_num, True, None)

    except subprocess.TimeoutExpired as e:
        error_msg = f"TIMEOUT: {e}"
        print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: {error_msg}")
        # Try to cleanup
        try:
            cleanup_cmd = ["ssh"] + ssh_opts + [ssh_host, "sudo", "nerdctl", "--namespace", "exe", "rm", "-f", container_name]
            subprocess.run(
                cleanup_cmd,
                capture_output=True,
                timeout=10
            )
        except:
            pass
        return (container_num, False, error_msg)
    except Exception as e:
        error_msg = f"EXCEPTION: {e}"
        print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] Container {container_num}: {error_msg}")
        return (container_num, False, error_msg)


def main():
    """
    Run multiple nerdctl operations in parallel to reproduce CNI race conditions.
    """
    # Configuration
    num_containers = 10  # Number of containers to run in parallel
    max_workers = 10     # Max parallel operations

    print(f"Starting race condition test with {num_containers} containers, {max_workers} workers")
    print(f"=" * 80)

    start_time = time.time()
    results = []

    # Run containers in parallel
    with ThreadPoolExecutor(max_workers=max_workers) as executor:
        futures = {
            executor.submit(run_nerdctl_container, i): i
            for i in range(num_containers)
        }

        for future in as_completed(futures):
            container_num, success, error = future.result()
            results.append((container_num, success, error))

    # Print summary
    elapsed = time.time() - start_time
    print(f"\n" + "=" * 80)
    print(f"Test completed in {elapsed:.2f} seconds")
    print(f"=" * 80)

    successes = sum(1 for _, success, _ in results if success)
    failures = sum(1 for _, success, _ in results if not success)

    print(f"\nResults:")
    print(f"  Successful: {successes}/{num_containers}")
    print(f"  Failed:     {failures}/{num_containers}")

    if failures > 0:
        print(f"\nFailure details:")
        for container_num, success, error in sorted(results):
            if not success:
                print(f"  Container {container_num}: {error}")
        print("\n✗ REPRODUCED: Race condition detected!")
        return 1
    else:
        print("\n✓ No race condition detected in this run")
        return 0


if __name__ == "__main__":
    sys.exit(main())
