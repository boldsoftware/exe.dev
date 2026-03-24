package execore

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"exe.dev/exemenu"
)

func (ss *SSHServer) handleRollCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	f, err := os.Open(home + "/assets/rr.txt")
	if err != nil {
		return fmt.Errorf("asset not found: %w", err)
	}
	defer f.Close()

	const rate = 1_705_948 // bytes per second
	const chunkSize = 32 * 1024
	interval := time.Second * chunkSize / rate

	buf := make([]byte, chunkSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		start := time.Now()
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, wErr := cc.Output.Write(buf[:n]); wErr != nil {
				return nil // client disconnected
			}
			if pause := interval - time.Since(start); pause > 0 {
				time.Sleep(pause)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
