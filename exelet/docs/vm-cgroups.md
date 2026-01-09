# VM Cgroup Management

This document describes how the exelet manages cgroups for VMs, including the per-account cgroup hierarchy for resource isolation.

## Overview

The exelet uses Linux cgroups v2 to manage and isolate VM resources. VMs are organized into a two-level hierarchy:

1. **Account slice**: Groups all VMs belonging to a single account
2. **VM scope**: Contains the individual VM process (cloud-hypervisor)

This design enables per-account resource control to address "noisy neighbor" issues, where one account's VMs could otherwise impact another account's performance.

## Cgroup Hierarchy

```
/sys/fs/cgroup/
└── exelet.slice/                           # Parent slice for all exelet-managed VMs
    ├── {group_id_1}.slice/                 # Per-group slice (e.g., vm0001.slice)
    │   ├── vm-{vm_id_a}.scope/             # VM scope (contains cloud-hypervisor process)
    │   └── vm-{vm_id_b}.scope/
    ├── {group_id_2}.slice/
    │   └── vm-{vm_id_c}.scope/
    └── default.slice/                      # Default slice for VMs without group_id
        └── vm-{vm_id_d}.scope/
```

### Naming Conventions

- **Group slice**: `{sanitized_group_id}.slice`
- **VM scope**: `vm-{sanitized_vm_id}.scope`
- **Sanitization**: Slashes (`/`) in IDs are replaced with underscores (`_`)

## Group ID Flow

The group ID (typically the account ID) flows from exed to exelet during VM creation:

1. **exed** looks up the user's account via `GetAccountByUserID(user.ID)`
2. **exed** includes `group_id` in the `CreateInstanceRequest` protobuf message
3. **exelet** stores `group_id` in the `Instance` config (persisted to disk)
4. **Resource manager** reads `group_id` from instances and creates appropriate cgroups

### Backwards Compatibility

VMs created before this feature (or without an account) use `group_id = ""`, which maps to the `default.slice`. This ensures:

- Existing VMs continue to work without migration
- No data migration is required
- The feature can be deployed without coordination between exed and exelet

## Migrating Existing VMs

Existing VMs in `account-default.slice` can be migrated to their proper account slice using the `SetInstanceGroup` RPC or the CLI command.

### CLI Migration

```bash
# Set group for a single instance
exelet-ctl --addr <exelet-addr> instances set-group --group <account_id> <instance_id>

# Set group for multiple instances
exelet-ctl --addr <exelet-addr> instances set-group --group <account_id> vm001 vm002 vm003
```

After setting the group ID, the resource manager will move the VM's cgroup to the new account slice on its next poll cycle (within 30 seconds by default).

### Migration Workflow

1. List instances on an exelet to identify VMs in the default slice:
   ```bash
   exelet-ctl --addr <exelet-addr> instances list
   ```

2. Set the group ID for each instance:
   ```bash
   exelet-ctl --addr <exelet-addr> instances set-group --group acct_123 vm000456-mybox
   ```

3. The resource manager will automatically move the cgroup on its next poll.

## Resource Manager

The resource manager (`exelet/services/resourcemanager/`) handles cgroup lifecycle:

### Initialization (`initControllers`)

At startup, the resource manager:
1. Enables required controllers (`cpu`, `io`, `memory`) at the cgroup root
2. Creates `/sys/fs/cgroup/exelet.slice/`
3. Enables controllers on the slice

### Account Slice Creation (`ensureAccountSlice`)

When a VM is first observed:
1. Creates the account slice directory if it doesn't exist
2. Enables controllers on the account slice
3. Logs the creation for observability

### VM Cgroup Creation (`ensureCgroup`)

For each VM:
1. Ensures the account slice exists
2. Creates the VM scope under the account slice
3. Moves the cloud-hypervisor process into the scope
4. Cleans up any old-style cgroups (migration from flat hierarchy)

### Priority Management (`applyPriority`)

The resource manager adjusts VM priority based on activity:

| Priority | CPU Weight | IO Weight | memory.high |
|----------|------------|-----------|-------------|
| NORMAL   | 100        | 100       | max         |
| LOW      | 50         | 50        | 80% of allocated |

VMs become LOW priority after being idle for the configured threshold (default: 1 minute).

### Cleanup (`removeCgroup`)

When a VM is removed:
1. Removes the VM's scope directory
2. Attempts to remove the account slice if empty
3. Cleans up any old-style cgroups that may exist

## Controllers

The following cgroup v2 controllers are enabled:

- **cpu**: Controls CPU scheduling weight via `cpu.weight`
- **io**: Controls I/O scheduling weight via `io.weight`
- **memory**: Controls memory limits and swap behavior via `memory.high` and `memory.swap.max`

## Configuration

The resource manager is configured via `ExeletConfig`:

| Setting | Default | Description |
|---------|---------|-------------|
| `ResourceManagerInterval` | 30s | Polling interval for usage collection |
| `IdleThreshold` | 1m | Duration after which inactive VMs become low priority |

## Future Enhancements

The per-account cgroup hierarchy enables future features:

1. **Per-account resource limits**: Set `cpu.max`, `memory.max`, `io.max` on account slices
2. **Account-level metrics**: Aggregate resource usage by account
3. **Fair scheduling**: Ensure accounts get fair share of resources regardless of VM count

## Files

| File | Description |
|------|-------------|
| `exelet/services/resourcemanager/priority.go` | Cgroup creation and priority management |
| `exelet/services/resourcemanager/resourcemanager.go` | Polling loop and state management |
| `exelet/services/resourcemanager/usage.go` | Usage collection (CPU, memory, disk, network) |
| `exelet/services/compute/create_instance.go` | VM creation with group_id persistence |
| `exelet/services/compute/set_group.go` | SetInstanceGroup RPC handler |
| `api/exe/compute/v1/compute.proto` | Protobuf definitions including group_id fields |
| `cmd/exelet-ctl/compute/instances/set_group.go` | CLI command for setting instance group |
