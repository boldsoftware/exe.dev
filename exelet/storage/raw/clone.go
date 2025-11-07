package raw

import (
	"context"
)

// Clone clones the source to the target
func (s *Raw) Clone(ctx context.Context, srcID string, destID string) error {
	return ErrNotImplemented
}
