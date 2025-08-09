# exe.dev - Container Service with Persistent Storage

exe.dev is a service that provides users with containers on GKE Autopilot with persistent disks. Users can SSH to exe.dev and get a guided console management tool to create and manage their containers.

## Features

- 🚀 **SSH-based Container Management** - Connect via SSH for an interactive console
- 🔐 **Public Key Authentication** - Secure SSH access with automatic registration
- ☁️ **GKE Autopilot Backend** - Containers run on Google Kubernetes Engine
- 💾 **Persistent Storage** - Each container gets its own persistent volume
- 🐳 **Custom Docker Images** - Build secure custom images from Dockerfiles
- 🔒 **Multi-tenant Security** - Isolated namespaces and resource limits

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

### Step 3: Create GKE Autopilot Cluster
```bash
# Create the cluster (takes ~5 minutes)
gcloud container clusters create-auto exe-autopilot \
    --location=us-central1
```

### Step 4: Create Service Account for Testing
```bash
# Create service account
gcloud iam service-accounts create exe-test \
    --display-name="exe.dev Test Account"

# Grant the 3 permissions we need
gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="serviceAccount:exe-test@PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/container.developer"

gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="serviceAccount:exe-test@PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/cloudbuild.builds.editor"

gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="serviceAccount:exe-test@PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/storage.objectAdmin"
```

### Step 5: Create and Download Service Account Key
```bash
# Create key file (this is what you'll set as env var)
gcloud iam service-accounts keys create exe-test-key.json \
    --iam-account=exe-test@PROJECT_ID.iam.gserviceaccount.com

# This creates exe-test-key.json in your current directory
```

### Step 6: Set Environment Variables
```bash
# Set these environment variables 
export GOOGLE_CLOUD_PROJECT="YOUR_PROJECT_ID"
export GOOGLE_APPLICATION_CREDENTIALS="/full/path/to/exe-test-key.json"
```

### Step 7: Test It Works
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
│   SSH Client    │───▶│   exed Server   │───▶│  GKE Autopilot  │
│                 │    │                 │    │                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                                │                       │
                                │               ┌─────────────────┐
                                └──────────────▶│  Cloud Build    │
                                                │  (Secure)       │
                                                └─────────────────┘
```

### Security Features

- **SSH Public Key Auth**: Automatic registration workflow
- **Namespace Isolation**: Each user gets their own Kubernetes namespace  
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
export GOOGLE_CLOUD_PROJECT="your-project-id"
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/key.json"
go test ./container/... -v
```

### Project Structure
```
├── cmd/exed/           # Main server binary
├── container/          # Container management package
│   ├── gke.go         # GKE Autopilot implementation
│   ├── build.go       # Secure Docker building
│   └── README.md      # Detailed container docs
├── exe.go             # Core SSH/HTTP server
└── exe_test.go        # SSH integration tests
```

## Cost Estimates

**Google Cloud costs for testing/development:**
- **GKE Autopilot**: ~$0.10/hour when idle
- **Cloud Build**: Free tier covers typical testing
- **Storage**: <$1/month for build artifacts
- **Total**: ~$75/month for 24/7 development cluster

**Production scaling:**
- Autopilot scales to zero when unused
- Pay only for active container resources
- No cluster management overhead

## Deployment

### Environment Variables

**Required:**
- `GOOGLE_CLOUD_PROJECT` - Your GCP project ID
- `GOOGLE_APPLICATION_CREDENTIALS` - Path to service account key

**Optional:**
- `EXE_GKE_CLUSTER_NAME` - Cluster name (default: "exe-autopilot")  
- `EXE_GKE_LOCATION` - Cluster location (default: "us-central1")
- `EXE_CONTAINER_REGISTRY` - Container registry (default: "gcr.io")
- `EXE_NAMESPACE_PREFIX` - Namespace prefix (default: "exe-")

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