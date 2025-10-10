# exe.dev - get a machine

EXE.DEV creates machines for you in the cloud, entirely over ssh.

Machines are containers running in VMs.
The exed binary acts as the control interface and the ssh/https proxy to the containers.

## Local machine setup

Start with the basics:

```
brew install tailscale coreutils lima
tailscale up
```

The underlying technology: containerd + kata + cloud hypervisor + nydus,
requires linux and requires KVM. There is no software emulation.
So you need at least an M3 CPU.

Once you have that, run:

```
./ops/setup-lima-hosts.sh
```

This sets up two VMs as ctr-hosts, one for running exed manually
and another as a ctr-host when running Go tests.

You can fast-wipe the ctr-hosts by running `./ops/reset-lima-hosts.sh`.

Then in your ~/.ssh/config add:

```
Host localexe
 HostName localhost
 Port 2222
 StrictHostKeyChecking no
 UserKnownHostsFile /dev/null

Host *.localexe
 HostName %h.localhost
 Port 2222
 StrictHostKeyChecking no
 UserKnownHostsFile /dev/null
```

## Local Development

After you have setup a local ctr host (see above) and downloaded the whoami database (make whoami), run:

```
go run ./cmd/exed -dev=local -gh-whoami $(pwd)/ghuser/whoami.sqlite3
```

With this you can:
- ssh localexe
- ssh <machine>@localexe
- visit http://localhost:8080
- visit http://machine.localhost:8080 (run `python -m http.server` in the machine first)
- scp junk.txt localexe:junk.txt

Everything will run locally on a lima VM.

To get details on the VM under your box, use commands like:

```
ssh lima-exe-ctr sudo nerdctl --namespace=exe ps -a
ssh lima-exe-ctr sudo nerdctl --namespace=exe logs <container ID>
```

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
go run ./cmd/exed -dev=local -https=:443
```

TLS requires valid domain names.
Exed uses `exe.local` instead of `localhost` for the main domain when serving TLS.
Certificates are issued by a local ACME server (Pebble) that runs automatically.

Add entries to `/etc/hosts`:

```
# exe.dev local dev
127.0.0.1	exe.local
127.0.0.1	www.exe.local
```

For each machine subdomain,
add its corresponding `.exe.local` entry.
For example, a machine named `testing`:

```
127.0.0.1	testing.exe.local
```

Custom domains work via CNAME records pointing to machine subdomains.
Create a CNAME record for your domain:

```
testing.bllamo.com.  CNAME  testing.exe.local.
```

Verify the CNAME:

```
dig +short testing.bllamo.com
```

Visit `https://testing.bllamo.com`.
Your browser will warn about untrusted certificates because Pebble's CA is not in your system trust store.
This is expected in local development.


## Production Deployment

### Regular deployment

Run:

```
make deploy-exed
```

This builds a new exed, pushes it to the VM, and restarts the service.

To see the commits that would ship before deploying, run `make deploy-what`.

To poke around production, ssh in using Tailscale:

```
ssh ubuntu@exed-prod-01
```

There are other deployment options, like `make deploy-piperd`, but these are less frequently used.

## Production Container Host Configuration

The script `./ops/setup-host-part1.sh` sets up more exe-ctr-NN hosts.

## Exeuntu Image

The default container image is `exeuntu`, which is a Ubuntu 24.04 image with development tools pre-installed. The image is hosted on GitHub Container Registry at `ghcr.io/boldsoftware/exeuntu:latest`.

To build and push the image:

```bash
# Build locally
make build-exeuntu

# Push to GitHub Container Registry (requires GitHub token with package write permissions)
echo $GITHUB_TOKEN | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
make push-exeuntu
```

The image is automatically built and pushed via GitHub Actions when the Dockerfile changes.

# Production Operations

Prod exed machine is `exed-01`. 

Keys are in `/etc/systemd/system/exed.service.d/env.conf`

Restart with `sudo systemctl daemon-reload && sudo systemctl restart exed`

Deploy with `make deploy-exed`

Systemd unit is in `/etc/systemd/system/exed.service` (TODO: source control)
