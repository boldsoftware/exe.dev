# exe.dev - get a machine

EXE.DEV creates machines for you in the cloud, entirely over ssh.

Machines are containers running in VMs.
The exed binary acts as the control interface and the ssh/https proxy to the containers.

## Local machine setup

Start with the basics:

```
brew install tailscale coreutils colima
tailscale up
```

The underlying technology: containerd + kata + cloud hypervisor + nydus,
requires linux and requires KVM. There is no software emulation.
So you need at least an M3 CPU.

Once you have that, run:

```
./ops/setup-colima-host.sh
```

For debugging you can `ssh exe-ctr-colima` and you can wipe it
by running `./ops/reset-colima.sh`.

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

After you have setup a local ctr host (see above), run:

```
go run ./cmd/exed -dev=local
```

With this you can:
- ssh localexe
- ssh <machine>@localexe
- visit http://localhost:8080
- visit http://machine.localhost:8080 (run `python -m http.server` in the machine first)
- scp junk.txt localexe:junk.txt

Everything will run locally on a colima VM.

## Production Deployment

### Regular deployment

Run:

```
make deploy
```

This will build a new exed, push it to the VM, and reboot it.

To poke around production, ssh in using Tailscale:

```
ssh ubuntu@exed-prod-01
```

## Initial production deployment

You probably don't need to do this.
This is to bring up the AWS infrastructure and the VM to run exed.
They should already exist.

First, get a Tailscale auth key from
https://login.tailscale.com/admin/settings/keys
**Important**: Create the key with `tag:server` tag for proper ACL management

```bash
# 1. Set up production VM with Docker and Tailscale
make setup-vm TAILSCALE_AUTH_KEY=tskey-auth-xxxxxxxxxxxxxx

# 2. Deploy the binary
make deploy

# 3. Check status
make status
```

The setup script will automatically:
- Set up a production VM with Ubuntu 22.04 LTS
- Install Docker for container management
- Configure Tailscale for secure access
- Install and configure systemd service for auto-start
- Set up versioned deployments for easy rollback

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
