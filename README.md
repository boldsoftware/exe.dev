# exe.dev - get a machine

EXE.DEV creates machines for you in the cloud, entirely over ssh.

Right now it is implemented on GKE, with a machine being a sandboxed container.

The exed binary acts as the control interface and the ssh/https proxy to the container.

## Local Development

```
go run ./cmd/exed -dev
```

With this you can:
- ssh -P 2222 localhost
- visit http://localhost:8080
- visit http://machine.team.localhost:8080
- scp -p 2222 junk.txt localhost:junk.txt  (NOTE: it's -p, not -P. yeah.)


## Production Deployment

## Local machine setup

```
brew install --cask gcloud-cli
gcloud auth login
```

## Regular deployment

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
This is to bring up the GKE cluster and the VM to run exed.
They should already exist.

First, get a Tailscale auth key from
https://login.tailscale.com/admin/settings/keys
**Important**: Create the key with `tag:server` tag for proper ACL management

```bash
# 1. Set up GKE cluster with gVisor sandbox (one-time setup)
./setup-gke-sandbox.sh

# 2. Set up production VM with Tailscale
make setup-vm TAILSCALE_AUTH_KEY=tskey-auth-xxxxxxxxxxxxxx

# 3. Deploy the binary
make deploy

# 4. Check status
make status
```

The setup scripts will automatically:
- Create a GKE Standard cluster with gVisor sandbox node pool
- Configure network policies for tenant isolation
- Set up a production VM with Ubuntu 22.04 LTS
- Configure Tailscale for secure access
- Install and configure systemd service for auto-start
- Set up versioned deployments for easy rollback
