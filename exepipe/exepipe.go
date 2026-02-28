// Package exepipe is a simple program that copies data between
// file descriptors. When exeprox or exed wants to set up a
// long-running connection, they will do so by handing the
// descriptors over to execopy.
// execopy will transfer data back and forth.
// This means that we can restart exed or exeprox without disturbing
// the existing connections.
package exepipe

import (
	"log/slog"
	"net"

	"exe.dev/stage"

	"github.com/prometheus/client_golang/prometheus"
)

// CopyConfig is the execopy configuration details.
type CopyConfig struct {
	Env             *stage.Env
	Logger          *slog.Logger
	UnixAddr        *net.UnixAddr // where to listen for commands
	MetricsRegistry *prometheus.Registry
}

// Copy is the running execopy instance.
type Copy struct {
	listener *net.UnixListener

	metrics *metrics

	lg       *slog.Logger
}

// NewCopy creates a new execopy instance.
func NewCopy(cgf *CopyConfig) (*Copy, error) {
	return nil, nil
}
