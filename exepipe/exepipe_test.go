package exepipe

import (
	"fmt"
	"math/rand/v2"
	"net"
	"testing"

	"exe.dev/stage"
	"exe.dev/tslog"

	"github.com/prometheus/client_golang/prometheus"
)

// testPipeInstance returns a PipeInstance for testing,
// and the Unix address for clients to connect to.
func testPipeInstance(t *testing.T) (*PipeInstance, *net.UnixAddr) {
	addr := &net.UnixAddr{
		Name: "@exepipetest" + fmt.Sprintf("%04x", rand.Uint32()&0xffff),
		Net:  "unixpacket",
	}

	pc := &PipeConfig{
		Env:             new(stage.Test()),
		Logger:          tslog.Slogger(t),
		UnixAddr:        addr,
		HTTPPort:        "0",
		MetricsRegistry: prometheus.NewRegistry(),
	}

	pi, err := NewPipe(pc)
	if err != nil {
		t.Fatal(err)
	}

	return pi, addr
}
