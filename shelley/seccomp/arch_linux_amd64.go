//go:build linux && amd64

package seccomp

import "golang.org/x/sys/unix"

const (
	auditArch          = unix.AUDIT_ARCH_X86_64
	sysKill            = 62
	sysTkill           = 200
	sysTgkill          = 234
	sysPidfdSendSignal = 424
)
