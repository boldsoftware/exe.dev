# Kata + CNI Bridge Plugin Race Condition Analysis

## Executive Summary

The race condition occurs in the **interaction between Kata and the CNI bridge plugin**, specifically when Kata tries to attach network interfaces created by CNI. Multiple concurrent container creations cause Kata's netlink queries to fail.

## Root Cause Analysis

### The Sequence of Events

1. **CNI Phase** (Serialized by our patch ✅):
   - CNI bridge plugin creates veth pair
   - One end goes to host bridge, one end prepared for container
   - CNI returns success with interface MAC address

2. **Kata Phase** (NOT serialized ❌):
   - Kata receives interface info (MAC address, IP, etc.)
   - Kata calls `update_interface` to configure the interface inside the VM
   - **RACE HAPPENS HERE**: Kata queries netlink to find the interface by MAC address

### The Kata Code (netlink.rs:106-113)

```rust
pub async fn update_interface(&mut self, iface: &Interface) -> Result<()> {
    // The reliable way to find link is using hardware address
    // as filter. However, hardware filter might not be supported
    // by netlink, we may have to dump link list and then find the
    // target link.
    let link = self.find_link(LinkFilter::Address(&iface.hwAddr)).await?;
    // ...
}
```

### The find_link Implementation (netlink.rs:260-296)

```rust
async fn find_link(&self, filter: LinkFilter<'_>) -> Result<Link> {
    let request = self.handle.link().get();
    let mut stream = filtered.execute();

    let next = if let LinkFilter::Address(addr) = filter {
        let mac_addr = parse_mac_address(addr)?;

        // ⚠️ RACE CONDITION: Dumps ALL links, filters client-side
        stream
            .try_filter(|f| {
                let result = f.attributes.iter().any(|n| match n {
                    Nla::Address(data) => data.eq(&mac_addr),
                    _ => false,
                });
                future::ready(result)
            })
            .try_next()
            .await?
    } else {
        stream.try_next().await?
    };

    // If not found, returns: "Link not found (Address: XX:XX:XX:XX:XX:XX)"
    next.map(|msg| msg.into())
        .ok_or_else(|| anyhow!("Link not found ({})", filter))
}
```

### Why It Races

When multiple containers start concurrently:

1. Multiple Kata VMs simultaneously query netlink for link lists
2. Netlink operations can return `ErrDumpInterrupted` when the network state changes during enumeration
3. If a link is created/deleted/modified during the dump, the results may be incomplete
4. The client-side filtering (by MAC) may not find the interface even though it exists

**The comment in the code admits this**: "Hardware filter might not be supported by netlink, we may have to dump link list and then find the target link."

## Version Analysis

### Current Versions
- **CNI bridge plugin**: v1.5.1 → v1.8.0 (upgraded)
- **Kata runtime**: v3.20.0 → v3.22.0 (upgraded)

### CNI Plugins v1.8.0 Improvements

Significant improvements between v1.5.1 and v1.8.0:

1. **Commit b088cc31**: "Move calls to netlinksafe"
   - All netlink calls in bridge.go now use `netlinksafe` wrappers
   - Properly retries on `ErrDumpInterrupted`
   - Should reduce (but not eliminate) the race

2. **Commit 0464017a**: "Add linting rule to block use of unsafe netlink calls"
   - Prevents future regressions
   - Enforces safe netlink usage

3. **Commit d924f05e**: "Update github.com/vishvananda/netlink to 1.3.0"
   - Updated netlink library with better error handling

### Changes in bridge.go (v1.5.1 → v1.8.0)

```diff
-	l, err := netlink.LinkByName(name)
+	l, err := netlinksafe.LinkByName(name)

-	hostVeth, err := netlink.LinkByName(hostIface.Name)
+	hostVeth, err := netlinksafe.LinkByName(hostIface.Name)

-	addrs, err := netlink.AddrList(br, family)
+	addrs, err := netlinksafe.AddrList(br, family)
```

All netlink operations now retry on interruption instead of failing immediately.

## Why My File-Lock Patch Helped (But Not Completely)

My patch added flock-based serialization to CNI bridge operations:

```go
func acquireCNILock() (*os.File, error) {
    lockFile, err := os.OpenFile("/var/run/cni-bridge.lock", os.O_CREATE|os.O_RDWR, 0644)
    syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
    // ...
}
```

**What this fixes:**
- ✅ Serializes CNI ADD/DEL operations
- ✅ Prevents concurrent veth pair creation/deletion
- ✅ Reduces netlink state churn during CNI operations

**What this doesn't fix:**
- ❌ Kata's asynchronous `update_interface` calls still happen concurrently
- ❌ Multiple Kata VMs racing to query netlink state
- ❌ The time window between CNI completion and Kata interface attachment

## Recommended Solutions

### Option 1: Upgrade to CNI Plugins v1.8.0 ⭐ **RECOMMENDED FIRST STEP**

**Pros:**
- Simple upgrade, no code changes
- Better netlink safety with automatic retries
- May significantly reduce the race window
- Low risk (well-tested upstream)

**Cons:**
- May not completely eliminate the race
- Still relies on Kata's netlink queries being reliable

**Implementation:**
```bash
# Build v1.8.0 plugins
cd /home/philip/cni-plugins
git checkout v1.8.0
go build -o /tmp/bridge ./plugins/main/bridge

# Install on VM
scp /tmp/bridge ubuntu@192.168.122.10:/tmp/
ssh ubuntu@192.168.122.10 "sudo cp /opt/cni/bin/bridge /opt/cni/bin/bridge.v1.5.1.bak && sudo mv /tmp/bridge /opt/cni/bin/bridge"
```

### Option 2: Extend Application-Level Locking

Keep my CNI bridge patch AND add locking in your Go code to serialize the entire container creation including Kata startup.

**In container/nerdctl.go**:
```go
// Extend perHostCreateLimit to cover the full lifecycle
func (nc *NerdctlContainerImplementation) CreateContainer(...) {
    nc.acquireCreateSlot(host) // Already exists
    defer nc.releaseCreateSlot(host)

    // All of:
    // 1. nerdctl run (creates veth via CNI)
    // 2. Wait for container running
    // 3. Wait for Kata to attach interface
    // Now serialized per-host
}
```

**Pros:**
- Guaranteed to work
- No external dependencies
- Already have partial implementation

**Cons:**
- Reduces concurrency (but you already have perHostCreateLimit=2)
- Slower test execution

### Option 3: Switch to tc-redirect-tap CNI Plugin

tc-redirect-tap is designed specifically for Kata/Firecracker and may handle concurrency better.

**Pros:**
- Purpose-built for Kata
- Different architecture may avoid the race

**Cons:**
- Requires reconfiguration
- Unknown if it actually solves the problem
- More complex setup

### Option 4: Patch Kata's find_link with Retry Logic

Add retry logic to Kata's netlink queries:

```rust
async fn find_link(&self, filter: LinkFilter<'_>) -> Result<Link> {
    // Retry up to 5 times with backoff
    for attempt in 0..5 {
        match self.find_link_once(filter).await {
            Ok(link) => return Ok(link),
            Err(e) if attempt < 4 => {
                tokio::time::sleep(Duration::from_millis(100 * (attempt + 1))).await;
                continue;
            }
            Err(e) => return Err(e),
        }
    }
}
```

**Pros:**
- Addresses root cause in Kata
- Could be upstreamed

**Cons:**
- Requires building custom Kata agent
- Complex deployment

## Recommended Action Plan

### Phase 1: Quick Win (15 minutes)
1. Upgrade CNI plugins to v1.8.0
2. Test with `go test -parallel=10`
3. Measure improvement

### Phase 2: If Phase 1 Not Sufficient (1 hour)
1. Keep my file-lock CNI patch (on v1.8.0)
2. Extend `perHostCreateLimit` to cover full container lifecycle
3. Test again

### Phase 3: If Still Issues (Research)
1. Investigate tc-redirect-tap
2. Consider Kata patch
3. Report upstream to Kata project

## Test Commands

```bash
# Test with v1.8.0 CNI plugins
PKGS=$(go list ./... | grep -v 'exe.dev/e1e/restarttest')
CTR_HOST=ssh://ubuntu@192.168.122.10 go test -count=1 -parallel=10 -timeout=10m $PKGS

# Specific container tests
CTR_HOST=ssh://ubuntu@192.168.122.10 go test -count=1 -parallel=10 -v ./container

# High stress test
CTR_HOST=ssh://ubuntu@192.168.122.10 go test -count=10 -parallel=20 -failfast ./container
```

## References

- CNI Plugins v1.8.0: https://github.com/containernetworking/plugins/releases/tag/v1.8.0
- Kata netlink.rs (analysis based on v3.20.0): https://github.com/kata-containers/kata-containers/blob/3.20.0/src/agent/src/netlink.rs#L260
- Kata v3.22.0 release: https://github.com/kata-containers/kata-containers/releases/tag/3.22.0
- netlinksafe package: https://github.com/containernetworking/plugins/blob/main/pkg/netlinksafe/netlink.go
