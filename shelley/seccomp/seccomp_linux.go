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
// (tkill, tgkill).
// This must be called before spawning child processes.
// The filter is inherited by child processes.
//
// The filter is installed with SECCOMP_FILTER_FLAG_TSYNC to synchronize
// across all threads in the process, ensuring child processes spawned
// from any goroutine will inherit the filter.
func BlockKillSelf() error {
	pid := uint32(os.Getpid())
	// Negative PID in two's complement (for blocking kill(-pid, sig) which
	// sends signals to the process group)
	negPid := uint32(-int32(pid))

	// Build BPF filter program that blocks kill/tkill/tgkill
	// when arg0 (target pid) matches our pid or -pid.
	//
	// The filter structure:
	// 1. Load and check architecture
	// 2. Load syscall number
	// 3. Check if it's one of the signal-sending syscalls
	// 4. If so, check if arg0 == our pid OR arg0 == -our pid
	// 5. If targeting us, return EPERM; otherwise allow
	filter := []unix.SockFilter{
		// [0] Load architecture
		bpfStmt(bpfLD|bpfW|bpfABS, offsetArch),
		// [1] If not our arch, jump to allow (end of filter)
		bpfJump(bpfJMP|bpfJEQ|bpfK, auditArch, 0, 12), // skip to ALLOW at [14]

		// [2] Load syscall number
		bpfStmt(bpfLD|bpfW|bpfABS, offsetNr),

		// [3] Check for kill
		bpfJump(bpfJMP|bpfJEQ|bpfK, sysKill, 4, 0), // match -> check pid at [8]
		// [4] Check for tkill
		bpfJump(bpfJMP|bpfJEQ|bpfK, sysTkill, 3, 0), // match -> check pid at [8]
		// [5] Check for tgkill (arg0 is tgid, arg2 is tid - we check arg0)
		bpfJump(bpfJMP|bpfJEQ|bpfK, sysTgkill, 2, 0), // match -> check pid at [8]

		// [6-7] Jump to allow for non-matching syscalls
		bpfJump(bpfJMP|bpfJEQ|bpfK, 0xFFFFFFFF, 0, 7), // never matches, always jumps to ALLOW at [14]
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW),  // [7] unreachable filler

		// [8] Load first argument (target PID) - lower 32 bits
		bpfStmt(bpfLD|bpfW|bpfABS, offsetArgs),
		// [9] Check if target PID matches our PID (positive)
		bpfJump(bpfJMP|bpfJEQ|bpfK, pid, 3, 0), // if our pid, jump to EPERM at [13]
		// [10] Check if target PID matches -our PID (for process group kills)
		bpfJump(bpfJMP|bpfJEQ|bpfK, negPid, 2, 0), // if -our pid, jump to EPERM at [13]

		// [11] Not targeting us, allow
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW),

		// [12] Unreachable filler
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ALLOW),

		// [13] Return EPERM for signal syscalls targeting our process
		bpfStmt(bpfRET|bpfK, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)),

		// [14] Allow the syscall
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

	// Use seccomp() syscall with SECCOMP_FILTER_FLAG_TSYNC to apply the filter
	// to all threads in the process. This ensures that child processes spawned
	// from any goroutine (which may run on different OS threads) will inherit
	// the filter.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER, TSYNC): %w", errno)
	}

	return nil
}
