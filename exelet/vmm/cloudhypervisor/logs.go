package cloudhypervisor

import (
	"context"
	"io"
	"os"
)

// Logs returns an io.ReadCloser for the instance logs
func (v *VMM) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	bootLogPath := v.bootLogPath(id)
	if _, err := os.Stat(bootLogPath); err != nil {
		return nil, err
	}
	return os.Open(bootLogPath)
}
