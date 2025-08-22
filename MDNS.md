# How -mdns Works

The `-mdns` flag enables **Multicast DNS (mDNS)** registration for local development. Here's the technical breakdown:

## 1. mDNS Service Registration
- When enabled, exed registers itself as `exe.local` on the local network using mDNS
- For each running machine, it registers additional hostnames like `{machine}.{team}.exe.local`
- Uses the `github.com/hashicorp/mdns` library for mDNS implementation

## 2. Unique IP Allocation
- Each machine gets a unique IP address from the 127.0.0.x loopback range (e.g., 127.0.0.2, 127.0.0.3, etc.)
- The IP pool uses the range [127.0.0.2, 127.0.0.255] for machine allocation
- mDNS maps `{machine}.{team}.exe.local` → unique loopback IP
- The main exed server listens on all these IPs and routes based on the target IP
- Uses 127.0.0.x loopback addresses for local development

### Setup Loopback Addresses

These loopback IPs **don't exist by default on MacOS** - you need to run the setup command:
```bash
sudo ./cmd/setup-loopback/setup-loopback
```

This creates IP aliases for the range 127.0.0.2-127.0.0.255 by running commands like:
```bash
sudo ifconfig lo0 alias 127.0.0.2
sudo ifconfig lo0 alias 127.0.0.3
# ... etc for each IP in the range
```

Use `sudo ./cmd/setup-loopback/setup-loopback -d` to remove the aliases.

## 3. SSH Routing with sshpiperd
- **sshpiperd** listens on port 2222 and handles all incoming SSH connections
- Run `make run-sshpiper` to start sshpiperd locally
- When you SSH to `machine.team.exe.local:2222`, the flow is:
  1. Your system queries mDNS to resolve the hostname to the unique loopback IP
  2. sshpiperd receives the connection on the unique IP:2222
  3. The **piper.go plugin** makes routing decisions:
     - **Direct machine access**: Routes to container SSH port (if `machine.team` format)
     - **Shell/registration**: Routes to exed on port 2223 using ephemeral proxy keys
  4. sshpiperd forwards the connection to the appropriate destination

## 4. Architecture: IPAllocator Interface

The exe.Server uses the `IPAllocator` interface to abstract IP address allocation and mDNS registration:

- **IPAllocator interface** (`ip_allocation.go:17`): Defines methods for allocating/deallocating IPs and managing mDNS services
- **MDNSAllocator** (`ip_allocation.go:50`): Implementation that handles loopback IP allocation and mDNS registration
  - Manages the mapping of `{machine}.{team}.exe.local` hostnames to 127.0.0.x addresses
  - Registers each machine as an mDNS service for SSH access
  - Thread-safe with mutex protection for concurrent access
- **ProductionIPAllocator** (`ip_allocation.go:325`): Placeholder for production deployment (not yet implemented)

The Server uses `SetIPAllocator()` to configure which allocation strategy to use based on the environment.

## 5. No Configuration Required
- mDNS is automatically supported by macOS, Linux (with Avahi), and Windows
- No need to edit `/etc/hosts` or `~/.ssh/config`
- Works "out of the box" for local development

## 6. Supported Formats
The system supports both formats:
- `ssh -p 2222 -o ConnectTimeout=1 machine.team.exe.local` (hostname-based, routed directly to container. I use `-o ConnectTimeout=1` to work around some mdns latency-induced ssh connection errors that I've noticed on my laptop)
- `ssh machine.team@exe.local -p 2222` (username-based, routed to exed shell)

