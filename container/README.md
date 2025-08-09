# Container Backend Package

This package provides Google Cloud-based container management for exe.dev using:

- **GKE Autopilot** for container orchestration
- **Persistent Volume Claims (PVCs)** for persistent storage
- **Cloud Build** for secure, isolated Docker image building

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   exe Server    │───▶│  GKE Autopilot  │───▶│  Persistent     │
│                 │    │                 │    │  Volumes        │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │
         │               ┌─────────────────┐
         └──────────────▶│  Cloud Build    │
                         │  (Secure)       │
                         └─────────────────┘
```

## Security Model

### Image Building Security
- All Docker builds run in **Cloud Build** for complete isolation
- Multi-tenant Dockerfiles are sandboxed from each other
- Resource limits prevent abuse (30min timeout, 100GB disk)
- Base image allowlist prevents malicious images
- Dockerfile validation blocks dangerous operations:
  - No privileged containers
  - No root users
  - No system directory access
  - Must specify non-root USER

### Container Runtime Security
- Containers run in isolated Kubernetes namespaces per user
- Resource limits enforced (CPU, memory)
- No privileged access
- Persistent storage isolated per container

## Google Cloud Setup

### Required Services
Enable these APIs in your Google Cloud project:
```bash
gcloud services enable container.googleapis.com
gcloud services enable cloudbuild.googleapis.com
gcloud services enable storage.googleapis.com
```

### Infrastructure Setup

#### 1. Create GKE Autopilot Cluster
```bash
# Create the cluster
gcloud container clusters create-auto exe-autopilot \
    --location=us-central1 \
    --project=YOUR_PROJECT_ID

# Get credentials for kubectl access  
gcloud container clusters get-credentials exe-autopilot \
    --location=us-central1 \
    --project=YOUR_PROJECT_ID
```

#### 2. Create Cloud Storage Bucket for Builds
```bash
# Cloud Build will create this automatically, but you can pre-create:
gsutil mb -p YOUR_PROJECT_ID gs://YOUR_PROJECT_ID_cloudbuild
```

### Service Account & Permissions

For **production deployment** (running exed in GKE), create a service account:

```bash
# Create service account
gcloud iam service-accounts create exe-server \
    --display-name="exe.dev Server"

# Grant necessary permissions
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:exe-server@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/container.developer"

gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:exe-server@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/cloudbuild.builds.editor" 

gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:exe-server@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/storage.objectAdmin"
```

### Development & Testing Credentials

For **local development and tests**, you need a service account key:

```bash
# Create service account for testing
gcloud iam service-accounts create exe-test \
    --display-name="exe.dev Test"

# Grant permissions (same as above)
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:exe-test@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/container.developer"

gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:exe-test@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/cloudbuild.builds.editor"

gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:exe-test@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/storage.objectAdmin"

# Create and download key file
gcloud iam service-accounts keys create exe-test-key.json \
    --iam-account=exe-test@YOUR_PROJECT_ID.iam.gserviceaccount.com
```

### Environment Variables for Testing

Set these environment variables for running tests:

```bash
# Required for all operations
export GOOGLE_CLOUD_PROJECT="your-project-id"
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/exe-test-key.json"

# Optional - will use defaults if not set
export EXE_GKE_CLUSTER_NAME="exe-autopilot"
export EXE_GKE_LOCATION="us-central1"
export EXE_CONTAINER_REGISTRY="gcr.io"

# For integration tests - creates test resources in isolation
export EXE_TEST_NAMESPACE_PREFIX="exe-test-"
```

## Test Infrastructure Isolation

Tests create resources with special prefixes to isolate from production:

- **Namespaces**: `exe-test-*` instead of `exe-*`
- **Images**: Tagged with `test-build` 
- **Storage**: Uses separate test buckets when possible

All test resources are cleaned up automatically, but you can also clean up manually:

```bash
# Delete all test namespaces
kubectl get namespaces -l managed-by=exe.dev | grep exe-test | awk '{print $1}' | xargs kubectl delete namespace

# Delete test images
gcloud container images list --repository=gcr.io/YOUR_PROJECT_ID | grep test | xargs -I {} gcloud container images delete {} --force-delete-tags
```

## Usage Examples

### Basic Container Creation
```go
config := container.DefaultConfig("my-project-id")
manager, err := container.NewGKEManager(ctx, config)

req := &container.CreateContainerRequest{
    UserID: "user123",
    Name:   "my-container",
    Image:  "ubuntu", // Optional, defaults to ubuntu
}

container, err := manager.CreateContainer(ctx, req)
```

### Custom Dockerfile Build
```go
dockerfile := `FROM ubuntu:latest
RUN apt-get update && apt-get install -y python3
USER 1000
WORKDIR /app`

req := &container.CreateContainerRequest{
    UserID:     "user123", 
    Name:       "python-container",
    Dockerfile: dockerfile,
}

container, err := manager.CreateContainer(ctx, req)
// Container will be in StatusBuilding until image build completes
```

### Container Management
```go
// List user's containers
containers, err := manager.ListContainers(ctx, "user123")

// Get container details
container, err := manager.GetContainer(ctx, "user123", "container-id")

// Stop/Start container
err = manager.StopContainer(ctx, "user123", "container-id")
err = manager.StartContainer(ctx, "user123", "container-id")

// Get logs
logs, err := manager.GetContainerLogs(ctx, "user123", "container-id", 100)
```

## Testing

### Unit Tests
```bash
go test ./container/...
```

### Integration Tests (requires GCP setup)
```bash
# Ensure credentials are set
export GOOGLE_CLOUD_PROJECT="your-project-id"
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account-key.json"

# Run integration tests
go test ./container/... -tags=integration
```

Integration tests will:
1. Create a test GKE namespace
2. Deploy a real container with PVC
3. Test build functionality with Cloud Build
4. Clean up all resources

## Security Considerations

1. **Network Isolation**: Each user gets their own Kubernetes namespace
2. **Resource Limits**: CPU/memory limits prevent resource exhaustion
3. **Image Security**: Only approved base images + secure build process
4. **Storage Isolation**: Each container gets its own PVC
5. **Build Isolation**: Cloud Build provides complete build-time isolation
6. **No Privileged Access**: Containers cannot escalate privileges

## Production Deployment

When deploying to production:

1. Use Workload Identity instead of service account keys
2. Enable Pod Security Standards 
3. Set up monitoring and alerting for container resources
4. Configure backup policies for persistent volumes
5. Implement proper log aggregation
6. Set up network policies for additional isolation