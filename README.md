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

## Google Cloud Setup

Follow these minimal steps to set up the Google Cloud backend for container management:

### Step 1: Create Google Cloud Project (if needed)
```bash
# Create project (skip if you have one)
gcloud projects create exe-dev-PROJECT_ID --name="exe.dev"

# Set as default
gcloud config set project PROJECT_ID
```

### Step 2: Enable Required APIs
```bash
# Enable the 3 APIs we need
gcloud services enable container.googleapis.com
gcloud services enable cloudbuild.googleapis.com  
gcloud services enable storage.googleapis.com
```

### Step 3: Create GKE Standard Cluster with Enhanced Security
```bash
# Set variables
export PROJECT_ID="exe-dev-468515"
export CLUSTER_NAME="exe-cluster"
export REGION="us-west2"
export ZONE="us-west2-a"

# Create cluster with security hardening (takes ~5 minutes)
gcloud container clusters create $CLUSTER_NAME \
  --project=$PROJECT_ID \
  --zone=$ZONE \
  --cluster-version=latest \
  --enable-ip-alias \
  --enable-autoscaling \
  --min-nodes=1 \
  --max-nodes=10 \
  --machine-type=n2-standard-4 \
  --disk-size=100 \
  --disk-type=pd-standard \
  --enable-autorepair \
  --enable-autoupgrade \
  --enable-shielded-nodes \
  --shielded-secure-boot \
  --shielded-integrity-monitoring \
  --enable-network-policy \
  --enable-intra-node-visibility \
  --enable-private-nodes \
  --master-ipv4-cidr=172.16.0.0/28 \
  --maintenance-window-start=2024-01-01T00:00:00Z \
  --maintenance-window-end=2024-01-01T04:00:00Z \
  --maintenance-window-recurrence="FREQ=WEEKLY;BYDAY=SU" \
  --workload-pool=${PROJECT_ID}.svc.id.goog \
  --addons=GcePersistentDiskCsiDriver,GcpFilestoreCsiDriver \
  --logging=SYSTEM,WORKLOAD \
  --monitoring=SYSTEM,WORKLOAD,POD

# Create sandbox node pool with gVisor
gcloud container node-pools create sandbox-pool \
  --cluster=$CLUSTER_NAME \
  --zone=$ZONE \
  --project=$PROJECT_ID \
  --machine-type=n2-standard-4 \
  --disk-size=100 \
  --disk-type=pd-standard \
  --enable-autoscaling \
  --min-nodes=2 \
  --max-nodes=20 \
  --node-taints=sandbox.gke.io/runtime=gvisor:NoSchedule \
  --node-labels=sandbox.gke.io/runtime=gvisor \
  --sandbox type=gvisor \
  --shielded-secure-boot \
  --shielded-integrity-monitoring \
  --metadata disable-legacy-endpoints=true \
  --workload-metadata=GKE_METADATA \
  --max-pods-per-node=32
```

### Step 4: Authenticate and Set Permissions
```bash
# Authenticate with your user account (opens browser)
gcloud auth application-default login

# Grant yourself the needed permissions
gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="user:$(gcloud config get-value account)" \
    --role="roles/container.developer"

gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="user:$(gcloud config get-value account)" \
    --role="roles/cloudbuild.builds.editor"

gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="user:$(gcloud config get-value account)" \
    --role="roles/storage.objectAdmin"
```

### Step 5: Install GKE Auth Plugin
```bash
# Install the GKE authentication plugin (required for kubectl access)
gcloud components install gke-gcloud-auth-plugin
```

### Step 6: Get Cluster Credentials
```bash
# Get credentials for kubectl access (needed for tests)
gcloud container clusters get-credentials $CLUSTER_NAME \
    --zone=$ZONE \
    --project=$PROJECT_ID
```

### Step 7: Apply Network Policies for Isolation
```bash
# Create network policy for namespace isolation
cat <<EOF | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all-ingress
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
  egress:
  - to:
    - namespaceSelector: {}
    ports:
    - protocol: TCP
      port: 53
    - protocol: UDP
      port: 53
  - to:
    - podSelector: {}
EOF

# This will be applied per-namespace for user isolation
```

### Step 8: Set Environment Variables
```bash
# Set environment variables for the application
export GOOGLE_CLOUD_PROJECT="exe-dev-468515"
export GKE_CLUSTER_NAME="exe-cluster"
export GKE_CLUSTER_LOCATION="us-west2-a"
export ENABLE_SANDBOX="true"
export STORAGE_CLASS_NAME="standard-rwo"

# For wildcard TLS (if you have Porkbun API keys)
# export PORKBUN_API_KEY="your-api-key"
# export PORKBUN_SECRET_API_KEY="your-secret-key"
```

### Step 9: Test It Works
```bash
# Run the integration tests
go test ./container/... -v

# You should see the integration test run instead of skip
```

## Usage

### Command Line Options
```bash
./exed [options]

Options:
  -http string
        HTTP server address (default ":8080")
  -https string
        HTTPS server address (enables TLS with Let's Encrypt)
  -ssh string
        SSH server address (default ":2222")
```

### Examples

**Development (HTTP only):**
```bash
./exed -http=:8080 -ssh=:2222
```

**Production (with HTTPS):**
```bash
./exed -http=:8080 -https=:443 -ssh=:22
```

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
gcloud auth application-default login  # if not already done
export GOOGLE_CLOUD_PROJECT="your-project-id"
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

## Deployment

### Environment Variables

**Required:**
- `GOOGLE_CLOUD_PROJECT` - Your GCP project ID
- `GKE_CLUSTER_NAME` - Cluster name (default: "exe-cluster")
- `GKE_CLUSTER_LOCATION` - Cluster location (default: "us-west2-a")

**Security Settings:**
- `ENABLE_SANDBOX` - Enable gVisor sandbox (default: "true")
- `STORAGE_CLASS_NAME` - Storage class name (default: "standard-rwo")

**Optional TLS (for wildcard certificates):**
- `PORKBUN_API_KEY` - Porkbun API key for DNS challenge
- `PORKBUN_SECRET_API_KEY` - Porkbun secret API key

**Authentication:** Uses Application Default Credentials via `gcloud auth application-default login`

### Production Considerations

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

---

**Note**: Replace `PROJECT_ID` with your actual Google Cloud Project ID in all commands above.
