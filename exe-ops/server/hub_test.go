package server

import (
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testHub(t *testing.T) *Hub {
	t.Helper()
	return NewHub(slog.Default())
}

func TestHubConnectDisconnectBroadcast(t *testing.T) {
	h := testHub(t)
	sub := h.Subscribe()
	defer h.Unsubscribe(sub)

	h.AgentConnected("agent-1")

	select {
	case ev := <-sub.ch:
		if ev.Type != "status" {
			t.Fatalf("expected status event, got %q", ev.Type)
		}
		var sd StatusData
		if err := json.Unmarshal(ev.Data, &sd); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if sd.Name != "agent-1" || !sd.Online {
			t.Fatalf("expected agent-1 online, got %+v", sd)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for connect event")
	}

	if !h.IsAgentConnected("agent-1") {
		t.Fatal("agent-1 should be connected")
	}

	h.AgentDisconnected("agent-1")

	select {
	case ev := <-sub.ch:
		var sd StatusData
		if err := json.Unmarshal(ev.Data, &sd); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if sd.Name != "agent-1" || sd.Online {
			t.Fatalf("expected agent-1 offline, got %+v", sd)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for disconnect event")
	}

	if h.IsAgentConnected("agent-1") {
		t.Fatal("agent-1 should be disconnected")
	}
}

func TestHubSubscriberChannelFull(t *testing.T) {
	h := testHub(t)
	sub := h.Subscribe()
	defer h.Unsubscribe(sub)

	// Fill the subscriber channel (capacity 64).
	for i := 0; i < 64; i++ {
		h.Broadcast(Event{Type: "status", Data: json.RawMessage(`{}`)})
	}

	// This should not block — event is dropped.
	done := make(chan struct{})
	go func() {
		h.Broadcast(Event{Type: "status", Data: json.RawMessage(`{}`)})
		close(done)
	}()

	select {
	case <-done:
		// OK, did not block.
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on full channel")
	}
}

func TestHubConcurrentConnectDisconnect(t *testing.T) {
	h := testHub(t)
	sub := h.Subscribe()
	defer h.Unsubscribe(sub)

	// Drain subscriber channel to avoid blocking.
	go func() {
		for range sub.ch {
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		name := "agent-" + string(rune('a'+i%26))
		go func() {
			defer wg.Done()
			h.AgentConnected(name)
		}()
		go func() {
			defer wg.Done()
			h.AgentDisconnected(name)
		}()
	}
	wg.Wait()

	// Should not panic or deadlock — that's the test.
	agents := h.ConnectedAgents()
	t.Logf("agents remaining after concurrent test: %d", len(agents))
}

func TestHubConnectedAgentsSnapshot(t *testing.T) {
	h := testHub(t)

	h.AgentConnected("a")
	h.AgentConnected("b")
	h.AgentConnected("c")

	agents := h.ConnectedAgents()
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
	for _, name := range []string{"a", "b", "c"} {
		if !agents[name] {
			t.Fatalf("expected %q in connected agents", name)
		}
	}

	h.AgentDisconnected("b")
	agents = h.ConnectedAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents after disconnect, got %d", len(agents))
	}
	if agents["b"] {
		t.Fatal("b should not be connected")
	}
}
