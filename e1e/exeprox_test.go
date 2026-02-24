package e1e

import (
	"testing"
)

func TestExeprox(t *testing.T) {
	t.Parallel()
	testHTTPProxy(t, Env.servers.Exeprox.HTTPPort, Env.servers.Exeprox.ExtraPorts)
}
