//go:build linux && arm64

package seccomp

import "golang.org/x/sys/unix"

const (
	auditArch          = unix.AUDIT_ARCH_AARCH64
	sysKill            = 129
	sysTkill           = 130
	sysTgkill          = 131
	sysPidfdSendSignal = 424
)
