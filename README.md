# exe.dev - get a machine

EXE.DEV creates machines for you in the cloud, entirely over ssh.

Machines are containers running in VMs.
The exed binary acts as the control interface and the ssh/https proxy to the containers.

## Ports Quick Reference for Dev

In dev, typically the exed host is localhost, and ctr-host is lima-ctr-host.

| Process | Host | Ports |
|---------|------|-------|
| **sshpiper** | exed host | 2222 (ssh proxy) |
| **exed** | exed host | 2223 (direct ssh), 8080 (http), 8001-8008, 9999 (dev mode box proxy) |
| **exed piper plugin** | exed host | 2224 (grpc) |
| **exelet** | ctr-host | 9080 (grpc), 9081 (http debug/metrics) |

## Local machine setup

Start with the basics:

```
brew install tailscale coreutils lima node uv zstd
tailscale up
```

The underlying technology: cloud hypervisor, requires linux and requires KVM. There is no software emulation.
So you need at least an M3 CPU.

Once you have that, run:

```
./ops/setup-lima-hosts.sh all
```

This sets up two VMs as ctr-hosts, one for running exed manually
and another as a ctr-host (where exelet and cloud-hypervisor runs) when running Go tests.

You can fast-wipe the ctr-hosts by running `./ops/setup-lima-hosts.sh reset`.

Optionally, add this to your ~/.ssh/config:

```
Host localexe
 HostName localhost
 Port 2222
 StrictHostKeyChecking no
 UserKnownHostsFile /dev/null

Host *.localexe
 HostName %h.exe.cloud
 Port 2222
 StrictHostKeyChecking no
 UserKnownHostsFile /dev/null
```

## Local Development

You can run exed and exelet together as follows. This will build both exelet and exed, and start both,
and the logs will be intermixed. First build will be slow, but then the kernel build is cached.

```
LOG_LEVEL=debug go run ./cmd/exed -stage=local -gh-whoami $(pwd)/ghuser/whoami.sqlite3 -start-exelet
```

### Running exelet separately

First, build the local exelet:

```
make exelet
```

NOTE: on first run this will build the kernel and rovol so will take a few minutes.

Next, copy the exeletd over:

```
scp exeletd lima-exe-ctr.local:
```

And then run it:


```
limactl shell exe-ctr -- sudo ./exeletd \
  -D \
  --data-dir /data/exelet \
  --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" \
  --network-manager-address nat:///data/exelet/network \
  --runtime-address cloudhypervisor:///data/exelet/runtime \
  --listen-address tcp://:9080
  --exed-url http://$(ssh lima-exe-ctr.local getent ahostsv4 _gateway | grep _gateway | awk '{ print $1; }'")
```

The exelet serves debug endpoints (pprof, version, metrics) on port 9081 by default.
Access them at `http://localhost:9081/debug` or use `--http-addr` to change the port.

### Running exed separately

After you have setup a local exelet running and downloaded the whoami database (make whoami), run:

```
go run ./cmd/exed -stage=local -gh-whoami $(pwd)/ghuser/whoami.sqlite3 \
  -exelet-addresses tcp://127.0.0.1:9080
```

## Continuing local development...

With this you can:
- ssh localexe
- ssh <machine>@localexe
- visit http://localhost:8080
- visit http://machine.exe.cloud:8080 (run `python -m http.server` in the machine first)
- scp junk.txt localexe:junk.txt

Everything will run locally on a lima VM.

To get details on the VM under your box, use commands like:

```
ssh lima-exe-ctr sudo nerdctl --namespace=exe ps -a
ssh lima-exe-ctr sudo nerdctl --namespace=exe logs <container ID>
```

## CTR_HOST Background

In CI, CTR_HOST is set to a brand-new VM. Locally, it defaults to
lima-exe-ctr-tests, but, of course, you can re-arrange that. "-start-exelet"
doesn't use CTR_HOST and just defaults to lima-exe-ctr. That it
doesn't read an env variable is an accident, though I think
reading a different one would make sense.

Remmeber that SSH is used in two ways: (1) it's used to bootstrap exelet on th
machine that can run cloud-hypervisor and (2) it's used to connect to those
cloud-hypervisor VMs. In the second case, we're using the same hostname, but
the port is different, since exelet has mapped the SSH port on the guest to a
port on the exelet host. We control the SSH settings in the second case and the
keys are in the database. We use this path for SSH proper, for the HTTP proxy,
and for the terminal UI.

## whoami DB and GITHUB_TOKEN

The following downloads the whoami database. It'll prompt you to install some
Backblaze tools from brew.
```
make whoami
```

You can create yourself a fine-grained personal access token with NO permissions
(public repositories only) at https://github.com/settings/personal-access-tokens
and set it as GITHUB_TOKEN.

## TLS (locally)

Run exed with TLS enabled:

```
go run ./cmd/exed -stage=local -https=:443
```

TLS requires valid domain names.
Exed uses `exe.cloud` subdomains for VM when serving TLS locally.
`*.exe.cloud` resolves to `127.0.0.1`.
Certificates are issued by a local ACME server (Pebble) that runs automatically.

Custom domains work via CNAME records pointing to machine subdomains.
Create a CNAME record for your domain:

```
testing.bllamo.com.  CNAME  testing.exe.cloud.
```

Verify the CNAME:

```
dig +short testing.bllamo.com
```

Visit `https://testing.bllamo.com`.
Your browser will warn about untrusted certificates because Pebble's CA is not in your system trust store.
This is expected in local development.


## Production Deployment

### Deploying exed

Run:

```
./ops/deploy/deploy-exed-prod.sh
```

This builds a new exed, pushes it to the VM, and restarts the service.

To see the commits that would ship before deploying, run `./ops/deploy/deploy-what-exed.sh`.

To poke around production, ssh in using Tailscale:

```
ssh ubuntu@exed-02
```

### Deploying exelet

To deploy exelet to production or staging:

```
./ops/deploy/deploy-exelet-prod.sh <machine-name>
./ops/deploy/deploy-exelet-staging.sh <machine-name>
```

### Building exelet-fs

The exelet requires kernel and rovol filesystem images. These are stored in Backblaze and downloaded automatically by `make exelet-fs`. To build and package new images:

```
make package-exelet-fs
```

This builds the kernel, rovol, and exe-init, then packages them into `exelet-fs-$(GOARCH).tar.gz`. Upload the resulting tarball to Backblaze (`bold-exe` bucket) to update the cached images.

## Production Container Host Configuration

The script `./ops/deploy/setup-exelet-host.sh` sets up more exe-ctr-NN hosts.

## Exeuntu Image

The default container image is `exeuntu`, which is a Ubuntu 24.04 image with development tools pre-installed. The image is hosted on GitHub Container Registry at `ghcr.io/boldsoftware/exeuntu:latest`.

The image is automatically built and pushed via GitHub Actions when files in `exeuntu/` or `shelley/` change. See `.github/workflows/build-exeuntu.yml` for details.

# Production Operations

Prod exed machine is `exed-02`.

Keys are in `/etc/systemd/system/exed.service.d/env.conf`

Restart with `sudo systemctl daemon-reload && sudo systemctl restart exed`

Deploy with `./ops/deploy/deploy-exed-prod.sh`

Systemd unit is in `/etc/systemd/system/exed.service` (TODO: source control)
