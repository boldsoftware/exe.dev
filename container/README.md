# Container Backend Package

This package provides Docker-based container management for exe.dev.

## Architecture

The container management system uses Docker with support for multiple Docker hosts:

- **Local Docker** for development
- **Remote Docker hosts** for production deployment on AWS EC2
- **Persistent volumes** via Docker volumes
- **Container isolation** through Docker's built-in features

## Usage Examples

### Basic Container Creation
```go
config := &container.Config{
    DockerHosts: []string{""}, // Local Docker
    DefaultCPURequest: "500m",
    DefaultMemoryRequest: "1Gi",
    DefaultStorageSize: "10Gi",
}
manager, err := container.NewDockerManager(config)

req := &container.CreateContainerRequest{
    UserID: "user123",
    Name:   "my-container",
    Image:  "ubuntu", // Optional, defaults to ubuntu
}

container, err := manager.CreateContainer(ctx, req)
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

// Execute command in container
err = manager.ExecuteInContainer(ctx, "user123", "container-id", 
    []string{"ls", "-la"}, stdin, stdout, stderr)
```

## Docker Host Configuration

### Local Development
```bash
# Uses local Docker daemon by default
./exed -dev=local
```

### Production with Remote Docker Hosts
```bash
# Single remote Docker host
./exed -docker-hosts tcp://docker1.example.com:2376

# Multiple Docker hosts for load distribution
./exed -docker-hosts tcp://docker1.example.com:2376,tcp://docker2.example.com:2376

# Via environment variable
export DOCKER_HOST=tcp://docker1.example.com:2376
./exed
```

## Testing

### Unit Tests
```bash
go test ./container/...
```

### Integration Tests (requires Docker)
```bash
go test ./container/... -v
```

## Security Considerations

1. **Container Isolation**: Each container runs in isolation with resource limits
2. **Resource Limits**: CPU/memory limits prevent resource exhaustion
3. **Storage Isolation**: Each container gets its own Docker volume
4. **Network Isolation**: Containers use Docker's network isolation features
5. **No Privileged Access**: Containers run without privileged mode