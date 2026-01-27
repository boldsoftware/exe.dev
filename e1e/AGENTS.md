# Running e1e Tests

## Standard Way (Using Lima VM)

If e1e tests aren't working, check that you can SSH to the test VM:

```bash
ssh lima-exe-ctr-tests.local echo "connected"
```

Then run tests:

```bash
go test -count=1 -run TestName -v ./e1e/...
```

## Running with CTR_HOST=localhost (On exe.dev VMs)

On exe.dev VMs (or any Linux machine with KVM, ZFS, and cloud-hypervisor), you can run e1e tests without a separate ctr-host by setting `CTR_HOST=localhost`. This runs exelet locally instead of over SSH.

### Prerequisites (auto-bootstrapped)

The test infrastructure will automatically:
- Install `zfsutils-linux` if missing
- Create a ZFS pool named "tank" from a sparse file at `/tmp/tank.img`
- Download `cloud-hypervisor` if missing
- Start `systemd-udevd` if not running
- Configure hugepages

### Running Tests

```bash
export PATH="$HOME/.local/bin:$PATH"
CTR_HOST=localhost go test -count=1 -run TestName -v ./e1e/...
```

### Examples

```bash
# Run a single box management test
CTR_HOST=localhost go test -count=1 -run TestNewWithEnvVars -v ./e1e/...

# Run all vanilla box subtests
CTR_HOST=localhost go test -count=1 -run TestVanillaBox -v ./e1e/...

# Run with debug logging
CTR_HOST=localhost go test -count=1 -run TestVanillaBox -v ./e1e/... -vexed -vexelet
```

### Cleanup

If tests leave processes running:

```bash
pkill -f "exed-test" 2>/dev/null || true
sudo pkill -f "exelet-test" 2>/dev/null || true
```

### Notes

- Each test run uses isolated ZFS datasets and network bridges (named `e1e-<testRunID>`)
- The ZFS pool preserves cached container images between runs for faster subsequent tests
- Test datasets are cleaned up automatically, but the pool and image cache persist
