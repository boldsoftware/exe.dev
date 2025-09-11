#!/bin/bash
set -e

# Check if we should run systemd
if [[ "$1" == "/sbin/init" ]] || [[ "$1" == "systemd" ]] || [[ $# -eq 0 ]]; then
    
    echo "Setting up systemd for container execution..."
    
    # Test systemd requirements step by step
    missing_caps=()
    suggested_args=()
    issues=()
    
    # Test 1: Can we mount tmpfs?
    # Use a temporary directory for testing to avoid conflicts with existing mounts
    if mkdir -p /tmp/mount-test 2>/dev/null && mount -t tmpfs tmpfs /tmp/mount-test 2>/dev/null; then
        umount /tmp/mount-test 2>/dev/null || true
        rmdir /tmp/mount-test 2>/dev/null || true
        echo "✓ SYS_ADMIN capability available"
    else
        missing_caps+=("SYS_ADMIN")
        suggested_args+=("--cap-add SYS_ADMIN")
        issues+=("Missing capability: SYS_ADMIN (cannot mount tmpfs)")
        echo "⚠️  Missing capability: SYS_ADMIN (cannot mount tmpfs)"
    fi
    
    # Test 2: Can we access cgroup properly?
    cgroup_ok=false
    if [[ -w /sys/fs/cgroup ]]; then
        # Test if we can mount cgroup2 on the tmpfs location
        # Use a test directory to avoid conflicts
        if mkdir -p /tmp/cgroup-test 2>/dev/null && mount -t cgroup2 none /tmp/cgroup-test 2>/dev/null; then
            umount /tmp/cgroup-test 2>/dev/null || true
            rmdir /tmp/cgroup-test 2>/dev/null || true
            cgroup_ok=true
            echo "✓ cgroup2 mounting capability verified"
        else
            issues+=("Cannot mount cgroup2 filesystem")
            echo "⚠️  Cannot mount cgroup2 filesystem"
        fi
    else
        issues+=("cgroup filesystem not writable")
        echo "⚠️  cgroup filesystem not writable"
    fi
    
    # Test 3: Are we PID 1?
    if [[ $$ -ne 1 ]]; then
        echo "⚠️  Not running as PID 1 (current PID: $$)"
        issues+=("Not running as PID 1")
    else
        echo "✓ Running as PID 1"
    fi
    
    # If we have critical issues, suggest the right command
    if [[ ${#issues[@]} -gt 0 ]]; then
        echo ""
        echo "🔧 SYSTEMD SETUP ISSUE DETECTED"
        echo "   This container requires additional Docker capabilities to run systemd properly."
        echo ""
        echo "   Recommended approach (non-privileged with proper capabilities):"
        echo "   docker run --cap-add=SYS_ADMIN --security-opt seccomp=unconfined --security-opt apparmor=unconfined --cgroupns=private --tmpfs /run --tmpfs /run/lock --tmpfs /tmp --tmpfs /sys/fs/cgroup:rw <other-options> \$DOCKER_IMAGE"
        echo ""
        echo "   Alternative (full privileges, easier but less secure):"
        echo "   docker run --privileged --tmpfs /tmp --tmpfs /run --tmpfs /run/lock <other-options> \$DOCKER_IMAGE"
        echo ""
        echo "   Attempting systemd startup anyway..."
        echo ""
    else
        echo "✓ All systemd requirements met"
	echo ""
        echo "   To detach from the container, use: Ctrl+P, Ctrl+Q"
        echo "   To attach back to a detached container: docker attach <container-name>"
        echo ""
        echo ""
    fi
    
    # Set up environment based on what we can do
    # TODO(philip): Is this the right choice in an exe.dev environment.
    if [ -f /.dockerenv ]; then
	export container=docker
    fi 
    
    if mount -t tmpfs tmpfs /tmp 2>/dev/null; then
        echo "Setting up with tmpfs mounts..."
        umount /tmp 2>/dev/null || true
        mount -t tmpfs tmpfs /tmp
        mount -t tmpfs tmpfs /run 2>/dev/null || mkdir -p /run
        mount -t tmpfs tmpfs /run/lock 2>/dev/null || mkdir -p /run/lock
    else
        echo "Setting up with regular directories..."
        mkdir -p /tmp /run /run/lock
        chmod 1777 /tmp
        chmod 755 /run /run/lock
    fi
    
    # Common setup
    mkdir -p /run/systemd /var/lib/systemd
    
    # Run the setup script after tmpfs mounts are ready
    /usr/local/bin/setup-systemd-container
    
    echo "Starting systemd..."
    exec /sbin/init
else
    # Run the provided command
    exec "$@"
fi
