# CI VM Testing Guide

This only works on linux; usually on the ci host!

## Starting an Ephemeral VM

To start a test VM for development:

```bash
NAME=my-test-vm ./ops/ci-vm-start.sh
```

This will:
1. Create a new ephemeral VM with the specified NAME
2. Use cached snapshots if available (based on ops/ git tree hash and date)
3. Install only what's needed: Cloud Hypervisor + exelet
4. Cache container images in ZFS
5. Output an envfile (e.g., `my-test-vm.env`) with VM details

## Running Tests Against the VM

Once the VM is running, you can run end-to-end tests against it:

```bash
# Get the VM IP from the envfile
source my-test-vm.env

# Run a specific test
CTR_HOST="ssh://ubuntu@${VM_IP}" go test -count=1 -run=TestVanillaBox ./e1e

# Or run all e1e tests
CTR_HOST="ssh://ubuntu@${VM_IP}" go test -count=1 ./e1e
```
