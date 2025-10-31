# CNI Networking Race Condition Investigation

## Problem Summary

Tests fail when run with `go test -parallel=N` (N > 1) but pass with `-parallel=1`. The issue is a **race condition in the CNI bridge plugin** when multiple nerdctl containers are created concurrently.

## Reproduced Error

```
failed to create shim task: "update interface: Link not found (Address: 22:ea:39:49:19:d7)
```

This error occurs when the CNI plugin tries to manipulate a network interface that has been deleted or doesn't exist due to concurrent operations.

## Evidence

1. **Python reproduction script** (`reproduce_nerdctl_race.py`):
   - With 50 concurrent containers: 39/50 failures (timeouts)
   - With 20 concurrent containers: occasional failures
   - All failures due to nerdctl commands hanging or CNI errors

2. **Go test reproduction**:
   - Running `TestContainerIntegrationSuite` with `-parallel=10`
   - Error: "update interface: Link not found"
   - Confirms CNI plugin race condition

## Current Configuration

- **CNI Plugin**: bridge plugin with host-local IPAM
- **Network**: nerdctl0 bridge (10.4.0.0/16)
- **Runtime**: Kata containers (io.containerd.kata.v2)
- **Timeout**: 120 seconds (in CNI config)

Configuration file: `/etc/cni/net.d/nerdctl-bridge.conflist`

## Root Cause

The CNI bridge plugin is **not fully concurrency-safe**. When multiple containers are created/deleted simultaneously:

1. Multiple CNI plugin invocations race to create/delete veth pairs
2. Network interface manipulation (ip link add/del) lacks proper serialization
3. Operations fail with "Link not found" when interfaces are deleted between check and use

## Potential Solutions

### Option 1: Serialize nerdctl operations at application level ⭐ RECOMMENDED

**Pros:**
- Minimal changes to infrastructure
- Guaranteed to work
- Easy to implement and test

**Cons:**
- Reduces parallelism (but may still be better than current state with timeouts)
- Doesn't fix underlying CNI issue

**Implementation:**
- Add a mutex/semaphore around nerdctl run/stop/rm operations
- Already have `perHostCreateLimit` in nerdctl.go (line 407)
- Could extend to also cover stop/rm operations

**Code changes needed:**
```go
// In nerdctl.go, extend the semaphore to cover all network operations
// Currently only CreateContainer has acquireCreateSlot()
// Need to add similar locking for StopContainer and DeleteContainer
```

### Option 2: Use a different CNI plugin

**Alternatives to consider:**
- **tc-redirect-tap**: Designed specifically for Kata, may have better concurrency
- **macvlan/ipvlan**: Avoids bridge complexity, but needs different network setup
- **host networking**: Simplest but loses isolation (not recommended for production)

**Pros:**
- May resolve race condition at plugin level
- Could improve performance

**Cons:**
- Requires significant testing
- May have other limitations
- Unknown if concurrency is actually better

### Option 3: Patch/configure CNI for better concurrency

**Options:**
- Increase CNI timeout (currently 120s, already high)
- Use CNI file locking (`.cni-concurrency.lock` exists but may not be sufficient)
- Contribute a fix to the CNI bridge plugin upstream

**Pros:**
- Fixes root cause
- Benefits wider community

**Cons:**
- Time-consuming
- Requires deep CNI knowledge
- May not be accepted upstream

### Option 4: Use nerdctl with a global lock file

**Implementation:**
```bash
# Wrapper around nerdctl commands
flock /var/lock/nerdctl.lock nerdctl run ...
```

**Pros:**
- External to Go code
- Easy to test

**Cons:**
- Requires wrapper scripts
- All nerdctl operations serialized (even non-conflicting ones)

## Recommended Approach

**Phase 1: Quick Fix (Recommended for now)**
1. Extend the existing `perHostCreateLimit` semaphore to also cover `StopContainer` and `DeleteContainer`
2. Reduce `perHostCreateLimit` from 2 to 1 for maximum safety
3. This will serialize all CNI operations, preventing the race condition

**Phase 2: Optimization (Future)**
1. Profile to understand actual bottlenecks
2. Consider alternative CNI plugins (tc-redirect-tap for Kata)
3. Contribute fixes upstream if needed

## Testing

To verify the fix:
```bash
# Python reproduction test
python3 reproduce_nerdctl_race.py

# Go tests with high parallelism
CTR_HOST=ssh://ubuntu@192.168.122.10 go test -count=1 -parallel=10 ./container

# Full test suite
PKGS=$(go list ./... | grep -v 'exe.dev/e1e/restarttest')
CTR_HOST=ssh://ubuntu@192.168.122.10 go test -count=1 -parallel=10 $PKGS
```

## References

- CNI specification: https://www.cni.dev/docs/spec/
- Kata Containers networking: https://github.com/kata-containers/kata-containers/blob/main/docs/design/networking.md
- Known CNI bridge plugin issues: https://github.com/containernetworking/plugins/issues
