# Exelet

Exelet is the compute agent/worker node in the exe.dev service that manages VM-based container instances. 
It handles the creation and lifecycle management of lightweight VMs running OCI container images.

## Protobuf
The exelet uses gRPC for transport. To generate the definitions, you will need the following installed:

- protobuf
- protoc-gen-go
- protoc-gen-go-grpc

You can install these with `brew install protobuf protoc-gen-go protoc-gen-go-grpc`.

In addition, we also use `go-fix-acronym` as a helper. You can install that with the following:

`go install github.com/containerd/protobuild/cmd/go-fix-acronym@latest`.

## Architecture Overview

Exelet is a gRPC server with a modular plugin-based architecture built on three main subsystems:

- **Storage Manager** - Persistent disk management (ZFS or raw disk images)
- **Network Manager** - Networking via Linux bridges, TAP devices, and NAT
- **VMM (Virtual Machine Monitor)** - Cloud Hypervisor integration for VM lifecycle

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Exelet Server                                  │
│                         (gRPC API - Port 8080)                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
                    ▼               ▼               ▼
        ┌──────────────────┐ ┌─────────────┐ ┌────────────────┐
        │ Storage Manager  │ │   Network   │ │      VMM       │
        │   (Pluggable)    │ │   Manager   │ │  (Cloud HV)    │
        └──────────────────┘ └─────────────┘ └────────────────┘
                │                   │                 │
        ┌───────┴────────┐          │                 │
        │                │          │                 │
        ▼                ▼          ▼                 ▼
    ┌───────┐      ┌───────┐   ┌──────┐      ┌──────────────┐
    │  ZFS  │      │  Raw  │   │ NAT  │      │Cloud Hyper-  │
    │Volume │      │ Disk  │   │DHCP  │      │visor Process │
    │+Crypto│      │ Image │   │Bridge│      │  (Per VM)    │
    └───────┘      └───────┘   └──────┘      └──────────────┘
        │              │           │                  │
        ▼              ▼           ▼                  ▼
    ┌────────────────────────────────────────────────────────────┐
    │                    VM Instance (Isolated)                  │
    │  ┌──────────────────────────────────────────────────────┐  │
    │  │              Container Rootfs (ext4)                 │  │
    │  │  ┌────────────────────────────────────────────────┐  │  │
    │  │  │  OCI Image Layers + Custom Init + SSH Keys     │  │  │
    │  │  └────────────────────────────────────────────────┘  │  │
    │  └──────────────────────────────────────────────────────┘  │
    │                                                            │
    │  Network: TAP device → Bridge → NAT → External             │
    │  Storage: virtio-blk → ZFS/Raw Volume                      │
    │  Shares:  virtio-fs  → Host directories                    │
    └────────────────────────────────────────────────────────────┘


Instance Creation Flow:
═══════════════════════

    Client gRPC Request
            │
            ▼
    ┌─────────────────┐
    │ Compute Service │
    └─────────────────┘
            │
            ├─[1]─► Storage Manager
            │       └─► Create volume (ZFS/Raw)
            │       └─► Format as ext4
            │       └─► Mount to temp path
            │
            ├─[2]─► Image Manager
            │       └─► Fetch container image
            │       └─► Extract to mounted volume
            │       └─► Fetch kernel + init images
            │
            ├─[3]─► Configuration
            │       └─► Write /etc/ssh/authorized_keys
            │       └─► Write /etc/hostname, /etc/hosts
            │       └─► Write /etc/resolv.conf
            │       └─► Write /etc/exe.dev/{env,image.conf}
            │       └─► Unmount volume
            │
            ├─[4]─► Network Manager
            │       └─► Create TAP device (tap-xxxxx)
            │       └─► Reserve IP via DHCP
            │       └─► Attach to bridge
            │
            └─[5]─► VMM (Cloud Hypervisor)
                    └─► Start cloud-hypervisor API process
                    └─► Create VM config (CPU/Mem/Disk/Net)
                    └─► Boot VM with kernel
                    └─► Return instance metadata
                            │
                            ▼
                    ┌────────────────┐
                    │ Running VM     │
                    │ Instance Ready │
                    └────────────────┘


VM Instance Runtime:
════════════════════

    Host Path: <runtime-data-dir>/cloudhypervisor/<instance-id>/
        │
        ├─► config.json              (VM configuration)
        ├─► cloud-hypervisor.sock    (API Unix socket)
        └─► boot.log                 (Console output)

    VM Process Tree:
        cloud-hypervisor (API mode)
            │
            └─► VM vCPU threads (KVM)

        virtiofsd (per shared directory)
            └─► Filesystem sharing via virtio-fs

    VM Resources:
        - CPUs:   Configurable (default: 1)
        - Memory: Configurable (default: 1GB)
        - Disk:   virtio-blk → /dev/zvol/... or loop device
        - Net:    virtio-net → TAP device
        - Shares: virtio-fs  → Host directories
```

## Storage Management

Location: `exelet/storage/`

### Interface

```go
type StorageManager interface {
    Create(ctx context.Context, req *CreateFilesystemRequest) (*CreateFilesystemResponse, error)
    Mount(ctx context.Context, req *MountFilesystemRequest) (*MountFilesystemResponse, error)
    Unmount(ctx context.Context, req *UnmountFilesystemRequest) error
    Delete(ctx context.Context, req *DeleteFilesystemRequest) error
}
```

### ZFS Storage (`storage/zfs/`)

**Features**:
- ZFS volume creation with configurable size
- Optional AES-256-GCM encryption support
- Automatic ext4 formatting
- Device path: `/dev/zvol/<dataset>/<instance-id>`

**Configuration**:
```
zfs:///var/tmp/exelet/storage?dataset=tank
```

**Key Files**:
- `zfs.go` - Manager initialization
- `create.go` - Volume creation with optional encryption
- `mount.go` - Volume mounting
- `utils.go` - ZFS operations and encryption key management

### Raw Storage (`storage/raw/`)

**Features**:
- Raw disk image files (`disk.img`)
- Loop device setup and management
- Automatic filesystem formatting
- No encryption support (TBD)

**Configuration**:
```
raw:///var/tmp/exelet/storage?state-dir=/run/exe/storage/raw
```

**Key Files**:
- `raw.go` - Manager initialization
- `create.go` - Disk image creation
- `mount.go` - Loop device mounting
- `utils.go` - Loop device management via ioctl

## Network Management

Location: `exelet/network/`

### NAT Network Manager (`network/nat/`)

**Features**:
- Linux bridge creation (`br-exe` by default)
- TAP device provisioning for VMs
- Built-in DHCP server for IP assignment
- IPTables-based NAT and forwarding
- Default network: `192.168.70.0/24`
- DNS: `1.1.1.1` (configurable)

**Configuration**:
```
nat:///path/to/data?bridge=br-exe&network=192.168.70.0/24&dns=1.1.1.1
```

**Network Data Path**:
```
VM eth0 → TAP device → Bridge (br-exe) → iptables NAT → External network
```

**Key Files**:
- `nat.go` - NAT manager with DHCP server
- `create_linux.go` - TAP device creation with DHCP reservations
- `configure_linux.go` - IPTables configuration for NAT/forwarding
- `start_linux.go` - Network startup sequence

**Startup Process**:
1. Configure bridge network device
2. Start DHCP server in background
3. Apply IPTables forwarding rules
4. Apply IPTables NAT masquerade rules

## VMM Interface (Cloud Hypervisor)

Location: `exelet/vmm/cloudhypervisor/`

### Interface

```go
type VMM interface {
    Create(ctx context.Context, req *CreateVMRequest) (*CreateVMResponse, error)
    Start(ctx context.Context, req *StartVMRequest) (*StartVMResponse, error)
    Stop(ctx context.Context, req *StopVMRequest) (*StopVMResponse, error)
    Delete(ctx context.Context, req *DeleteVMRequest) error
    State(ctx context.Context, req *StateVMRequest) (*StateVMResponse, error)
    Logs(ctx context.Context, req *LogsVMRequest) (io.Reader, error)
}
```

### Cloud Hypervisor Integration

**Key Operations**:

**Create VM** (`create.go`):
- Saves VM configuration to JSON
- Starts Cloud Hypervisor API as background process
- Waits for API socket availability
- Configures virtiofs shares for directory mounting
- Creates VM via Cloud Hypervisor HTTP API

**Start VM** (`start.go`):
- Checks current VM state
- Restarts API instance if needed
- Boots VM via API

**VM Configuration** (`config.go`):
- CPU and memory allocation
- Root disk attachment (virtio-blk)
- Network interface (TAP device)
- Kernel and boot arguments with network config
- Virtiofs filesystem shares
- Console configuration (PTY)

**Key Files**:
- `cloudhypervisor.go` - VMM initialization and lifecycle
- `create.go` - VM creation workflow
- `start.go` - VM boot process
- `config.go` - Configuration translation
- `virtiofs.go` - Virtiofsd daemon management for filesystem sharing
- `client/` - Auto-generated Cloud Hypervisor HTTP API client

**Communication**:
- Unix socket to Cloud Hypervisor API
- Auto-generated client from OpenAPI spec
- Operations: CreateVM, BootVM, ShutdownVMM, etc.

## Compute Service

Location: `exelet/services/compute/`

The compute service implements the core instance lifecycle gRPC API.

### Instance Creation Workflow

**Complete flow** (`create_instance.go`):

1. **Initialization**
   - Generate UUID v7 for instance ID
   - Create instance directory

2. **Storage Setup**
   - Create filesystem via StorageManager
   - Mount filesystem
   - Setup cleanup handler

3. **Networking**
   - Create TAP device via NetworkManager
   - Reserve IP via DHCP manager
   - Generate network configuration

4. **Image Pulling**
   - Fetch and extract container image to root filesystem
   - Fetch kernel image (custom Linux kernel)
   - Fetch init image (custom init system)

5. **Guest Configuration**
   - Write SSH authorized keys to `/etc/ssh/authorized_keys`
   - Set hostname in `/etc/hostname`
   - Configure `/etc/hosts` with instance IP
   - Write nameservers to `/etc/resolv.conf`
   - Write environment variables to `/etc/exe.dev/env`
   - Write image config to `/etc/exe.dev/image.conf`

6. **VM Creation and Boot**
   - Unmount filesystem
   - Generate boot arguments with network config
   - Create VM via VMM
   - Start VM

7. **Completion**
   - Save instance configuration
   - Return instance metadata to client

### Other Operations

- **StartInstance** - Boot existing VM
- **StopInstance** - Graceful shutdown
- **DeleteInstance** - Remove VM and cleanup resources
- **GetInstance** - Retrieve instance details
- **ListInstances** - List all instances
- **UpdateInstance** - Modify VM configuration
- **LogsInstance** - Access boot logs

## How Components Work Together

```
gRPC CreateInstance Request
    ↓
Compute Service
    ↓
Storage Manager → Create & mount ZFS/raw volume
    ↓
Image Manager → Fetch container image, kernel, init
    ↓
Configuration → Write SSH keys, hostname, resolv.conf, etc.
    ↓
Network Manager → Create TAP device, reserve IP
    ↓
VMM (Cloud Hypervisor) → Create and boot VM
    ↓
Instance Boot
```

## Configuration

### Command-Line Flags

```bash
exelet \
  --name local \
  --listen-address tcp://127.0.0.1:8080 \
  --data-dir /var/tmp/exelet \
  --region us-central \
  --zone 1a \
  --runtime-address cloudhypervisor:// \
  --network-manager-address nat:///var/tmp/exelet/network \
  --storage-manager-address zfs:///var/tmp/exelet/storage?dataset=tank \
  --enable-instance-boot-on-startup
```

### Key Paths

**Inside Guest VM**:
- SSH keys: `/etc/ssh/authorized_keys`
- Hostname: `/etc/hostname`
- Hosts: `/etc/hosts`
- DNS: `/etc/resolv.conf`
- Image config: `/etc/exe.dev/image.conf`
- Environment: `/etc/exe.dev/env`

**On Host**:
- ZFS volumes: `<storage-data-dir>/volumes/<id>/`
- ZFS mounts: `<storage-data-dir>/mounts/<id>/`
- Raw volumes: `<storage-data-dir>/volumes/<id>/disk.img`
- VM configs: `<data-dir>/runtime/cloudhypervisor/<id>/`
- API socket: `<data-dir>/runtime/cloudhypervisor/<id>/cloud-hypervisor.sock`

## Client Usage

```go
import "exe.dev/exelet/client"

// Create client
client, err := exelet.NewClient("tcp://localhost:8080",
    exelet.WithInsecure())

// Create instance
stream, err := client.CreateInstance(ctx, &api.CreateInstanceRequest{
    Name:        "test-instance",
    Image:       "docker.io/library/alpine:latest",
    CPUs:        1,
    Memory:      1 * 1000 * 1000 * 1000,  // 1GB
    Disk:        1 * 1000 * 1000 * 1000,  // 1GB
})

// Process status updates
for {
    resp, err := stream.Recv()
    if err == io.EOF {
        break
    }
    // Handle progress and final instance
}
```

## Summary

Exelet provides a complete VM orchestration system that:
- Runs OCI container images inside lightweight VMs
- Provides VM-level isolation with persistent storage
- Supports pluggable VMMs including Cloud Hypervisor for efficient virtualization
- Supports pluggable networking including Linux bridges and NAT
- Supports pluggable storage backends (ZFS, raw)
- Exposes gRPC API for lifecycle management
