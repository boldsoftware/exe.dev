package raw

import (
	"context"
)

// Clone clones the source to the target
func (s *Raw) Clone(ctx context.Context, srcID, destID string) error {
	return ErrNotImplemented
}
