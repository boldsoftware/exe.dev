# exe.dev - Multi-tenant Container Service with gVisor Sandbox

exe.dev is a secure multi-tenant container service that provides users with sandboxed containers on GKE with persistent storage. Users can SSH to exe.dev and get a guided console management tool to create and manage their containers.

## Features

- 🚀 **SSH-based Container Management** - Connect via SSH for an interactive console
- 🔐 **Public Key Authentication** - Secure SSH access with automatic registration
- ☁️ **GKE Standard with gVisor** - Containers run in sandboxed environment for multi-tenant isolation
- 💾 **Persistent Storage** - Each container gets its own persistent volume
- 🐳 **Custom Docker Images** - Build secure custom images from Dockerfiles
- 🔒 **Enhanced Multi-tenant Security** - gVisor sandbox, network policies, and isolated namespaces
- 🛡️ **Wildcard TLS Certificates** - Automatic HTTPS for all subdomains via DNS challenge

## Quick Start

### Prerequisites

- Go 1.24+
- Google Cloud SDK (`gcloud`)
- Google Cloud Project with billing enabled
- Tailscale account (for production deployment)

### Local Development

1. **Clone and build:**
   ```bash
   git clone <repo-url>
   cd exe
   go build -o exed ./cmd/exed
   ```

2. **Run locally (HTTP only):**
   ```bash
   ./exed -http=:8080 -ssh=:2222
   ```

3. **Test SSH connection:**
   ```bash
   ssh -p 2222 localhost
   ```

## Production Deployment

### Prerequisites

1. **Tailscale Account**: Get an auth key from https://login.tailscale.com/admin/settings/keys
   - **Important**: Create the key with `tag:server` tag for proper ACL management

2. **Google Cloud Project**: Must have billing enabled

### Quick Setup

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

### Access Methods

After setup, you can access the VM in two ways:

1. **Public Access** (for initial setup/debugging):
   ```bash
   ssh -p 22222 ubuntu@<external-ip>
   ```

2. **Tailscale Access** (recommended for daily operations):
   ```bash
   ssh ubuntu@exed-prod-01
   ```

### Production Commands

```bash
make deploy        # Build and deploy new version
make ssh-vm        # SSH to production VM
make logs          # View production logs
make status        # Check service status
make restart       # Restart the service
```

### Tailscale ACL Configuration

For proper security, configure your Tailscale ACL policy to restrict the `tag:server` tag:

```json
{
  "tagOwners": {
    "tag:server": ["autogroup:admin"]
  },
  "acls": [
    {
      "action": "accept",
      "src": ["autogroup:admin"],
      "dst": ["tag:server:*"]
    },
    {
      "action": "accept", 
      "src": ["tag:server"],
      "dst": ["tag:k8s:*"]
    }
  ]
}
```

## Google Cloud Setup (Manual)

If you prefer manual setup instead of using the scripts, see the detailed steps below:

<details>
<summary>Click to expand manual GKE setup instructions</summary>

### Step 1: Enable Required APIs
```bash
gcloud services enable container.googleapis.com
gcloud services enable cloudbuild.googleapis.com  
gcloud services enable storage.googleapis.com
```

### Step 2: Create GKE Cluster
See `setup-gke-sandbox.sh` for the complete cluster creation command with all security settings.

### Step 3: Authenticate
```bash
gcloud auth application-default login
```

### Step 4: Get Cluster Credentials
```bash
gcloud container clusters get-credentials exe-cluster \
    --zone=us-west2-a \
    --project=exe-dev-468515
```

</details>

## Usage

### SSH Commands

Once connected via SSH, you can use these container management commands:

- `containers` - List your containers
- `create-container [name]` - Create a new container
- `container-status <id>` - Get container status
- `container-logs <id>` - View container logs
- `help` - Show available commands
- `exit` - Exit the session

### Custom Docker Images

Create containers with custom Dockerfiles:

```bash
# In SSH session
create-container my-python-app
# Then provide a Dockerfile when prompted
```

Example secure Dockerfile:
```dockerfile
FROM python:3.9
RUN pip install flask
RUN useradd -m appuser
USER appuser
WORKDIR /home/appuser
```

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   SSH Client    │───▶│   exed Server   │───▶│  GKE Standard   │
│                 │    │                 │    │  with gVisor    │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                                │                       │
                                │               ┌─────────────────┐
                                └──────────────▶│  Cloud Build    │
                                                │  (Secure)       │
                                                └─────────────────┘
```

### Security Features

- **gVisor Sandbox**: Userspace kernel providing syscall filtering and isolation
- **SSH Public Key Auth**: Automatic registration workflow
- **Namespace Isolation**: Each user gets their own Kubernetes namespace
- **Network Policies**: Inter-namespace communication blocked by default
- **Shielded Nodes**: Secure boot and integrity monitoring
- **Private Cluster**: Private node IPs with controlled master access
- **Workload Identity**: Pod-level GCP authentication without keys
- **Resource Limits**: CPU and memory limits prevent resource exhaustion
- **Secure Building**: Custom images built in isolated Cloud Build environments
- **Dockerfile Validation**: Blocks privileged containers and dangerous operations
- **Base Image Allowlist**: Only trusted base images permitted

## Development

### Running Tests
```bash
# Unit tests
go test ./...

# Integration tests (requires GCP setup)
export GOOGLE_CLOUD_PROJECT="exe-dev-468515"
go test ./container/... -v
```

### Project Structure
```
├── cmd/exed/           # Main server binary
├── container/          # Container management package
│   ├── gke.go         # GKE implementation with sandbox support
│   ├── build.go       # Secure Docker building
│   └── manager.go     # Container manager interface
├── porkbun/           # DNS provider for wildcard TLS
│   ├── dns.go         # DNS challenge implementation
│   └── wildcard_cert.go # Wildcard certificate manager
├── exe.go             # Core SSH/HTTP server
└── exe_test.go        # SSH integration tests
```

### Environment Variables

**Required for Production:**
- `GOOGLE_CLOUD_PROJECT` - Your GCP project ID
- `GKE_CLUSTER_NAME` - Cluster name (default: "exe-cluster")
- `GKE_CLUSTER_LOCATION` - Cluster location (default: "us-west2-a")

**Security Settings:**
- `ENABLE_SANDBOX` - Enable gVisor sandbox (default: "true")
- `STORAGE_CLASS_NAME` - Storage class name (default: "standard-rwo")

**Optional TLS (for wildcard certificates):**
- `PORKBUN_API_KEY` - Porkbun API key for DNS challenge
- `PORKBUN_SECRET_API_KEY` - Porkbun secret API key

## Cost Estimates

**Google Cloud costs for testing/development:**
- **GKE Standard (2x n2-standard-4)**: ~$0.40/hour for minimum nodes
- **Cloud Build**: Free tier covers typical testing
- **Storage**: <$1/month for build artifacts
- **Total**: ~$300/month for 24/7 sandbox cluster with 2 nodes

**Production scaling:**
- Autoscaling from 2-20 nodes based on demand
- Pay for node resources (not per-pod like Autopilot)
- gVisor overhead: ~10-20% performance impact
- Better for multi-tenant workloads requiring strong isolation

## Production Considerations

1. **Use Workload Identity** instead of service account keys
2. **Enable Pod Security Standards** for additional security  
3. **Set up monitoring** for container resources and costs
4. **Configure backup policies** for persistent volumes
5. **Implement log aggregation** for troubleshooting
6. **Set up network policies** for additional isolation

## Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Run tests (`go test ./...`)
4. Commit your changes (`git commit -am 'Add amazing feature'`)
5. Push to the branch (`git push origin feature/amazing-feature`)
6. Open a Pull Request

## License

[Your License Here]