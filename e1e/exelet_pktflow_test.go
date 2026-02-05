package e1e

import (
	"testing"
	"time"

	pktflowapi "exe.dev/pkg/api/exe/pktflow/v1"
)

func TestPktFlowStream(t *testing.T) {
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.disconnect()

	boxName := newBox(t, pty)
	defer pty.deleteBox(boxName)

	waitForSSH(t, boxName, keyFile)

	// Get pktflow gRPC client via the exelet's existing connection.
	exeletClient := Env.servers.Exelets[0].Client()
	ctx := Env.context(t)

	pktflowClient := pktflowapi.NewPktFlowServiceClient(exeletClient.Conn())
	stream, err := pktflowClient.StreamFlowStats(ctx, &pktflowapi.StreamFlowStatsRequest{})
	if err != nil {
		t.Fatalf("StreamFlowStats: %v", err)
	}

	// Generate hermetic ICMP traffic inside the box.
	// Ping the default gateway — stays on the bridge, never leaves the host.
	pingCmd := boxSSHShell(t, boxName, keyFile, "ping -c 20 -i 0.1 $(ip route | awk '/default/{print $3}')")
	if err := pingCmd.Start(); err != nil {
		t.Fatalf("start ping: %v", err)
	}
	t.Cleanup(func() { pingCmd.Process.Kill(); pingCmd.Wait() })

	// Poll the stream until we see a report with ICMP flow data.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		report, err := stream.Recv()
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		for _, vm := range report.GetVms() {
			if vm.GetTxPackets() == 0 {
				continue
			}
			for _, flow := range vm.GetFlows() {
				if flow.GetIpProto() == 1 { // ICMP
					t.Logf("got ICMP flow: packets=%d bytes=%d dst=%s",
						flow.GetPackets(), flow.GetBytes(), flow.GetDstIP())
					return // success
				}
			}
		}
	}
	t.Fatal("timed out waiting for ICMP flow record in pktflow stream")
}
