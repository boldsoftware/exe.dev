package cloudhypervisor

import (
	"errors"
	"os/exec"
)

func backgroundInit() (string, []string, error) {
	initPath := "nohup"
	args := []string{}
	// attempt to locate tini to have proper cloud-hypervisor cleanup
	for _, exe := range []string{"tini", "tini-static"} {
		p, err := exec.LookPath(exe)
		if err != nil {
			if !errors.Is(err, exec.ErrNotFound) {
				return "", nil, err
			}
			continue
		}
		// found
		initPath = p
		args = []string{
			"-s",
			"--",
		}
		break
	}
	return initPath, args, nil
}
