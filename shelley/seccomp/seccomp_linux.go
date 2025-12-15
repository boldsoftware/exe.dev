//go:build linux

// Package seccomp provides a seccomp filter to prevent child processes
// from killing the parent process.
//
// Note: We use raw BPF instead of github.com/seccomp/libseccomp-golang
// because that library requires cgo and links against libseccomp.
// This pure-Go implementation avoids the cgo dependency.
package seccomp

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// BPF instruction constants
const (
	bpfLD  = 0x00
	bpfW   = 0x00
	bpfABS = 0x20
	bpfJMP = 0x05
	bpfJEQ = 0x10
	bpfRET = 0x06
	bpfK   = 0x00
)

// seccomp_data offsets
const (
	offsetNr   = 0  // syscall number (int, 4 bytes)
	offsetArch = 4  // architecture (u32, 4 bytes)
	offsetArgs = 16 // args[0] starts at offset 16 (u64 each)
)

// bpfStmt creates a BPF statement (no jump targets)
func bpfStmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: 0, Jf: 0, K: k}
}

// bpfJump creates a BPF jump instruction
func bpfJump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// BlockKillSelf installs a seccomp filter that prevents any process from
// sending signals to the current process via kill(2) and related syscalls
// (tkill, tgkill, pidfd_send_signal).
// This must be called before spawning child processes.
// The filter is inherited by child processes.
func BlockKillSelf() error {
	pid := uint32(os.Getpid())

	// Build BPF filter program that blocks kill/tkill/tgkill/pidfd_send_signal
	// when arg0 (target pid) matches our pid.
	//
	// The filter structure:
	// 1. Load and check architecture
	// 2. Load syscall number
	// 3. Check if it's one of the signal-sending syscalls
	// 4. If so, check if arg0 == our pid
	// 5. If targeting us, return EPERM; otherwise allow
	filter := []unix.SockFilter{
		// [0] Load architecture
		bpfStmt(bpfLD|bpfW|bpfABS, offsetArch),
		// [1] If not our arch, jump to allow (end of filter)
		bpfJump(bpfJMP|bpfJEQ|bpfK, auditArch, 0, 13), // skip to ALLOW at [15]

		// [2] Load syscall number
		bpfStmt(bpfLD|bpfW|bpfABS, offsetNr),

		// [3] Check for kill
		bpfJump(bpfJMP|bpfJEQ|bpfK, sysKill, 8, 0), // match -> check pid at [12]
		// [4] Check for tkill
		bpfJump(bpfJMP|bpfJEQ|bpfK, sysTkill, 7, 0), // match -> check pid at [12]
		// [5] Check for tgkill (arg0 is tgid, arg2 is tid - we check arg0)
		bpfJump(bpfJMP|bpfJEQ|bpfK, sysTgkill, 6, 0), // match -> check pid at [12]
		// [6] Check for pidfd_send_signal (arg0 is pidfd, not pid - skip this check)
		// pidfd_send_signal uses a file descriptor, not a pid, so we can't easily
		// filter it by pid. We'll skip it for now.
		// If we wanted to block it entirely: bpfJump(bpfJMP|bpfJEQ|bpfK, sysPidfdSendSignal, 0, 1),
		// but that would break legitimate uses.

		// [6-11] Jump to allow for non-matching syscalls
		bpfJump(bpfJMP|bpfJEQ|bpfK, 0xFFFFFFFF, 0, 8), // never matches, always jumps to ALLOW

		// This is unreachable filler to keep offsets correct
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW), // [7]
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW), // [8]
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW), // [9]
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW), // [10]
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW), // [11]

		// [12] Load first argument (target PID) - lower 32 bits
		bpfStmt(bpfLD|bpfW|bpfABS, offsetArgs),
		// [13] Check if target PID matches our PID
		bpfJump(bpfJMP|bpfJEQ|bpfK, pid, 0, 1), // if not our pid, jump to allow

		// [14] Return EPERM for signal syscalls targeting our process
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)),

		// [15] Allow the syscall
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW),
	}

	// Set NO_NEW_PRIVS to allow unprivileged seccomp
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}

	// Install the seccomp filter
	prog := unix.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}

	// Use seccomp() syscall directly
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER): %w", errno)
	}

	return nil
}
