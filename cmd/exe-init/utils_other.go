//go:build !linux

package main

func mountProc() error {
	return ErrNotImplemented
}

func mountDev() error {
	return ErrNotImplemented
}

func mountSysfs() error {
	return ErrNotImplemented
}

func cleanRun() error {
	return ErrNotImplemented
}

func applySysctl(_, _ string) error {
	return ErrNotImplemented
}

func getBootArg(_ string) (string, error) {
	return "", ErrNotImplemented
}
