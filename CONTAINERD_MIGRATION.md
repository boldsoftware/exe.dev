# Containerd Migration Guide

This document describes the migration from Docker to containerd for the exe.dev container runtime.

## Overview

The exe.dev service now supports containerd as an alternative to Docker for container management. Containerd offers:
- Better performance and lower resource usage
- Direct integration with container runtimes
- Simplified architecture without the Docker daemon overhead

## Configuration

### Backend Selection

The container backend can be specified using the `-container-backend` flag:

```bash
# Use containerd (default)
./exed -container-backend containerd

# Use Docker (for compatibility)
./exed -container-backend docker
```

### Host Configuration

#### Local Containerd
For local containerd, no host specification is needed:
```bash
./exed -container-backend containerd
```

#### Remote Containerd via SSH
Unlike Docker which supports `DOCKER_HOST` for TCP connections, containerd access to remote hosts is done via SSH:

```bash
# Single remote host
./exed -container-backend containerd -docker-hosts ubuntu@host1.example.com

# Multiple remote hosts
./exed -container-backend containerd -docker-hosts ubuntu@host1.example.com,ubuntu@host2.example.com

# Using SSH config aliases
./exed -container-backend containerd -docker-hosts dockerhost1,dockerhost2

# If the remote user needs sudo for ctr commands
export CTR_USE_SUDO=true
./exed -container-backend containerd -docker-hosts ubuntu@host1.example.com
```

The SSH access requires:
1. SSH key authentication configured
2. The user must have permissions to run `ctr` commands (or sudo configured)
3. Containerd must be installed and running on the remote host

After running the `ops/newdockerhost-part2-containerd-kata.sh` script:
- The ubuntu user is added to the containerd group
- Socket permissions are configured for group access
- Sudo is configured for passwordless ctr commands as a fallback
- You may need to log out and back in for group membership to take effect

#### Custom Socket Path
For local containerd with a non-standard socket:
```bash
./exed -container-backend containerd -docker-hosts /run/containerd/containerd.sock
```

## Server Setup

The `ops/newdockerhost.sh` script has been updated to install and configure containerd:

1. **Installs containerd and runc packages**
2. **Configures containerd** with CRI plugin enabled
3. **Sets up CNI networking** for container network management
4. **Installs nerdctl** for easier debugging (optional)

### Manual Setup

To manually set up a containerd host:

```bash
# Install containerd
sudo apt-get update
sudo apt-get install -y containerd runc

# Configure containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml

# Enable CRI plugin (remove from disabled_plugins)
sudo sed -i 's/disabled_plugins = \["cri"\]/disabled_plugins = \[\]/' /etc/containerd/config.toml

# Enable systemd cgroup driver
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

# Restart containerd
sudo systemctl restart containerd
sudo systemctl enable containerd

# Install CNI plugins
sudo mkdir -p /opt/cni/bin
curl -L https://github.com/containernetworking/plugins/releases/download/v1.4.0/cni-plugins-linux-$(uname -m)-v1.4.0.tgz | sudo tar -C /opt/cni/bin -xz

# Configure CNI
sudo mkdir -p /etc/cni/net.d
sudo tee /etc/cni/net.d/10-containerd-net.conflist > /dev/null <<EOF
{
  "cniVersion": "1.0.0",
  "name": "containerd-net",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "cni0",
      "isGateway": true,
      "ipMasq": true,
      "ipam": {
        "type": "host-local",
        "ranges": [
          [{"subnet": "10.88.0.0/16"}]
        ],
        "routes": [{"dst": "0.0.0.0/0"}]
      }
    },
    {
      "type": "portmap",
      "capabilities": {"portMappings": true}
    }
  ]
}
EOF

# Verify installation
sudo ctr version
```

## Key Differences

### Command Mapping

| Docker Command | Containerd (ctr) Command |
|---------------|-------------------------|
| `docker run` | `ctr run` or create container + start task |
| `docker ps` | `ctr tasks list` |
| `docker ps -a` | `ctr containers list` |
| `docker exec` | `ctr tasks exec` |
| `docker stop` | `ctr tasks kill` |
| `docker rm` | `ctr containers delete` |
| `docker pull` | `ctr images pull` |
| `docker logs` | Not directly available in ctr |
| `docker build` | Not available (use buildkit separately) |

### Architectural Differences

1. **Namespaces**: Containerd uses namespaces to isolate containers. The exe.dev implementation uses the "exe" namespace.

2. **Tasks vs Containers**: In containerd:
   - A "container" is a metadata/configuration object
   - A "task" is a running instance of a container
   - You must create a container first, then start a task from it

3. **Networking**: Currently using host networking for simplicity. CNI plugins provide more advanced networking.

4. **Remote Access**: No built-in TCP socket support like Docker's `DOCKER_HOST`. Remote access is via SSH.

## Limitations

Current limitations of the containerd implementation:

1. **Build Support**: Image building is not yet implemented (would require buildkit integration)
2. **Logs**: Container logs are not directly accessible via `ctr` (would need to implement log streaming)
3. **Networking**: Currently using host networking mode for simplicity

## Migration Path

To migrate from Docker to containerd:

1. **Install containerd** on your hosts using the setup script or manual steps
2. **Update configuration** to use containerd backend
3. **Test** with a subset of containers first
4. **Monitor** performance and stability
5. **Complete migration** once validated

## Troubleshooting

### Check containerd status
```bash
sudo systemctl status containerd
sudo ctr --namespace exe containers list
sudo ctr --namespace exe tasks list
```

### Test remote access
```bash
ssh user@remote-host ctr version
```

### Debug container issues
```bash
# List all containers in exe namespace
ctr --namespace exe containers list

# Check task status
ctr --namespace exe tasks list

# Inspect container
ctr --namespace exe containers info <container-id>
```

## Performance Benefits

Containerd typically offers:
- 30-50% lower memory usage compared to Docker daemon
- Faster container startup times
- Reduced system load
- Better stability for long-running containers