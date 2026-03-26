e1e is end-to-end tests for exe.dev.

These are the gold standard tests. Unit tests are great, but don't skip these!

e1e tests start real containers and exercise the full stack. They run locally, no special infrastructure required.

Always run e1e tests when editing related code. Do not skip them or claim they need special infrastructure.

e1e tests can be slow; targeting particular tests with -run will help.

## Running Tests (macOS, Lima VM)

On macOS, e1e tests use a lima VM (`lima-exe-ctr-tests`). If basic tests are failing out of the gate, see ops/setup-lima-hosts.sh.

## Running Tests (exe.dev VM / Linux with CTR_HOST=localhost)

On exe.dev VMs or Linux with KVM/ZFS/cloud-hypervisor, skip the VM:

```bash
export PATH="$HOME/.local/bin:$PATH"
CTR_HOST=localhost go test -count=1 -run TestName -v ./e1e/...
```

Prerequisites are auto-bootstrapped (zfsutils-linux, ZFS pool, cloud-hypervisor, systemd-udevd, hugepages).
