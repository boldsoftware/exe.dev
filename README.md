# exe.dev - get a machine

EXE.DEV creates machines for you in the cloud, entirely over ssh.

Machines are Docker containers running on AWS EC2 instances. The exed binary acts as the control interface and the ssh/https proxy to the containers.

## Local Development

```
go run ./cmd/exed -dev=local
```

With this you can:
- ssh -p 2222 localhost
- visit http://localhost:8080
- visit http://machine.localhost:8080
- scp -P 2222 junk.txt localhost:junk.txt  (NOTE: it's -P, not -p)

Everything will run locally on docker.

## Production Deployment

### Local machine setup

```
brew install tailscale
tailscale up
```

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

## Docker Host Configuration

In production, you can specify multiple Docker hosts for distributing containers:

```bash
# Using a single remote Docker host
./exed -docker-hosts tcp://docker1.example.com:2376

# Using multiple Docker hosts
./exed -docker-hosts tcp://docker1.example.com:2376,tcp://docker2.example.com:2376

# Or via environment variable
export DOCKER_HOST=tcp://docker1.example.com:2376
./exed
```

For development, it defaults to the local Docker daemon.

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
