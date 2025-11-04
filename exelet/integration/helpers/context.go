package helpers

import (
	"context"
	"time"
)

// GetContext returns a new context for the integration tests
func GetContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Second*60)
}
