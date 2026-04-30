// Vsock port assignments for services exe-init runs inside the guest.
//
// Operators reach these by speaking the cloud-hypervisor hybrid-vsock
// handshake ("CONNECT <port>\n") on the unix-domain socket CH binds on the
// host. Keep this file platform-independent so e1e and host code can import
// without dragging in linux-only deps.

package main

const (
	// OperatorSSHVsockPort is the AF_VSOCK port exe-init's operator SSH
	// server listens on inside the guest.
	OperatorSSHVsockPort = 2222

	// MemdVsockPort is the AF_VSOCK port memd listens on inside the guest.
	// memd is a tiny single-shot "GET memstat\n" → JSON server used by
	// exelet to scrape /proc/meminfo, /proc/pressure/memory, and curated
	// /proc/vmstat fields without going through SSH.
	MemdVsockPort = 2223
)
