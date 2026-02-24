package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestExeprox(t *testing.T) {
	t.Parallel()

	exeproxInstance, err := testinfra.StartExeprox(Env.context(t), Env.servers.Exed.HTTPPort, Env.servers.Exed.ExeproxPort, []int{0, 0}, testRunID, logFileFor("exeprox"), *flagVerbosePorts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		exeproxInstance.Stop(Env.context(t), testRunID)
		for line := range exeproxInstance.Errors {
			t.Errorf("exeprox ERROR: %s", line)
		}
	})

	testHTTPProxy(t, exeproxInstance.HTTPPort, exeproxInstance.ExtraPorts)
}
