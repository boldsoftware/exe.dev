//go:build !linux

package netns

import (
	"context"
	"fmt"
	"io"
)

func LiveStream(_ context.Context, _ io.Writer, _ string) error {
	return fmt.Errorf("live streaming requires linux")
}

func LiveStreamByVMID(_ context.Context, _ io.Writer, _ string) error {
	return fmt.Errorf("live streaming requires linux")
}
