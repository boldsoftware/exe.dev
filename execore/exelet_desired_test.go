package execore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"exe.dev/desiredstate"
	"exe.dev/exedb"
)

func TestExeletDesiredMissingHost(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest("GET", "/exelet-desired", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	server.handleExeletDesired(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestExeletDesiredUnknownHost(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest("GET", "/exelet-desired?host=tcp://unknown:9080", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	server.handleExeletDesired(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestExeletDesiredEmptyHost(t *testing.T) {
	server := newTestServer(t)

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	req := httptest.NewRequest("GET", "/exelet-desired?host="+addr, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	server.handleExeletDesired(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var ds desiredstate.DesiredState
	if err := json.NewDecoder(w.Body).Decode(&ds); err != nil {
		t.Fatal(err)
	}

	if len(ds.VMs) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(ds.VMs))
	}
	if len(ds.Groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(ds.Groups))
	}
}

func TestExeletDesiredWithBoxes(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "desired@example.com")

	// Create a box with 2 allocated CPUs
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "testbox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Set container_id and status to simulate a running VM
	containerID := "container-abc-123"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:          boxID,
			ContainerID: &containerID,
			Status:      "running",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/exelet-desired?host="+addr, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	server.handleExeletDesired(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var ds desiredstate.DesiredState
	if err := json.NewDecoder(w.Body).Decode(&ds); err != nil {
		t.Fatal(err)
	}

	if len(ds.VMs) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(ds.VMs))
	}

	vm := ds.VMs[0]
	if vm.ID != containerID {
		t.Errorf("VM ID = %q, want %q", vm.ID, containerID)
	}
	if vm.Group != userID {
		t.Errorf("VM group = %q, want %q", vm.Group, userID)
	}
	if vm.State != "running" {
		t.Errorf("VM state = %q, want \"running\"", vm.State)
	}

	// Should have cpu.max set for 2 CPUs: quota=200000, period=100000
	if len(vm.Cgroup) != 1 {
		t.Fatalf("expected 1 cgroup setting, got %d", len(vm.Cgroup))
	}
	if vm.Cgroup[0].Path != "cpu.max" {
		t.Errorf("cgroup path = %q, want \"cpu.max\"", vm.Cgroup[0].Path)
	}
	if vm.Cgroup[0].Value != "200000 100000" {
		t.Errorf("cgroup value = %q, want \"200000 100000\"", vm.Cgroup[0].Value)
	}

	// Should have one group for the user
	if len(ds.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(ds.Groups))
	}
	if ds.Groups[0].Name != userID {
		t.Errorf("group name = %q, want %q", ds.Groups[0].Name, userID)
	}
}

func TestExeletDesiredNoCpuMaxWhenAllocatedCpusNull(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "nocpus@example.com")

	// Create a box without allocated CPUs (legacy box, allocatedCPUs=0 means NULL)
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "legacybox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	containerID := "container-legacy-456"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:          boxID,
			ContainerID: &containerID,
			Status:      "running",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/exelet-desired?host="+addr, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	server.handleExeletDesired(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var ds desiredstate.DesiredState
	if err := json.NewDecoder(w.Body).Decode(&ds); err != nil {
		t.Fatal(err)
	}

	if len(ds.VMs) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(ds.VMs))
	}

	// No cpu.max when allocated_cpus is NULL
	if len(ds.VMs[0].Cgroup) != 0 {
		t.Errorf("expected 0 cgroup settings for legacy box, got %d: %v", len(ds.VMs[0].Cgroup), ds.VMs[0].Cgroup)
	}
}

func TestExeletDesiredSkipsBoxesWithoutContainerID(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "nocontainer@example.com")

	// Create a box but don't set container_id (simulates "creating" state)
	_, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "creatingbox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/exelet-desired?host="+addr, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	server.handleExeletDesired(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var ds desiredstate.DesiredState
	if err := json.NewDecoder(w.Body).Decode(&ds); err != nil {
		t.Fatal(err)
	}

	if len(ds.VMs) != 0 {
		t.Errorf("expected 0 VMs (no container_id), got %d", len(ds.VMs))
	}
}
