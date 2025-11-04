# Quickstart: Exelet Dev

For an overview, see `exelet/README.md.`

## Quickstart

To get the `exelet` up and running in a development environment, use the following:

- Configure a custom cloud-hypervisor and virtiofsd:
    - `limactl copy ./ops/setup-cloudhypervisor.sh exe-ctr:/tmp/setup-cloud-hypervisor.sh` 
    - `limactl shell exe-ctr -- sudo /tmp/setup-cloud-hypervisor.sh` 
- Build the `exelet`  (this will build the kernel, exe-init, and exe-ssh and embed them in the exelet - you will need docker installed to build the kernel - first time will take a few min)
    - `make GOOS=linux exelet` 
- Run `exelet` (best in another terminal window):

```go
limactl shell exe-ctr -- sudo ./exeletd \
  -D \
  --data-dir /data/exelet \
  --storage-manager-address raw:///data/exelet/storage \
  --network-manager-address nat:///data/exelet/network \
  --runtime-address cloudhypervisor:///data/exelet/runtime
```

Note: there is about a 10 second delay in startup due to IPTables. I’m investigating.

- Build `exelet-ctl` 
    - `make exelet-ctl` 
- List Instances:
    - `./exelet-ctl compute instances ls` 
- Create a test Alpine instance:
    - `./exelet-ctl compute instances create -i docker.io/library/redis:alpine --ssh-key ~/.ssh/id_ed25519.pub` 
- Get instance logs:
    - `./exelet-ctl compute instances logs <id>` 
- SSH to instance (need to be inside `exe-ctr` )
    - `ssh -i /path/to/id_ed25519 root@<instance-ip>` (working on getting name resolution via the ssh proxy, etc.)
- Run integration tests:
    - `go test -v ./exelet/integration/...` 

```go
=== RUN   TestComputeCreateAlpine
--- PASS: TestComputeCreateAlpine (2.80s)
=== RUN   TestComputeCreateValidateOutput
--- PASS: TestComputeCreateValidateOutput (7.19s)
=== RUN   TestComputeCreateValidateRedis
--- PASS: TestComputeCreateValidateRedis (14.20s)
PASS
ok      exe.dev/exelet/integration      24.534s
?       exe.dev/exelet/integration/helpers      [no test files]
```
