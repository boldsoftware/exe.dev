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
	// Group should have default cpu.max = 2x max allocated CPUs = 2*2 = 4 CPUs
	if len(ds.Groups[0].Cgroup) != 1 {
		t.Fatalf("expected 1 group cgroup setting, got %d: %v", len(ds.Groups[0].Cgroup), ds.Groups[0].Cgroup)
	}
	if ds.Groups[0].Cgroup[0].Path != "cpu.max" || ds.Groups[0].Cgroup[0].Value != "400000 100000" {
		t.Errorf("group cgroup = %v, want cpu.max:400000 100000", ds.Groups[0].Cgroup[0])
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

func TestExeletDesiredWithBoxCgroupOverrides(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "boxoverride@example.com")

	// Create a box with 2 allocated CPUs
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "overridebox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Set container_id and status
	containerID := "container-override-123"
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

	// Set a box-level cgroup override that overrides cpu.max and adds memory.high
	overrides := "cpu.max:10000 100000\nmemory.high:1073741824"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.SetBoxCgroupOverrides(ctx, exedb.SetBoxCgroupOverridesParams{
			CgroupOverrides: &overrides,
			ID:              boxID,
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
	// The override should replace cpu.max (from allocated_cpus=2 → 200000 100000)
	// with the override value (10000 100000), and add memory.high.
	if len(vm.Cgroup) != 2 {
		t.Fatalf("expected 2 cgroup settings, got %d: %v", len(vm.Cgroup), vm.Cgroup)
	}

	// Find settings by path
	cgMap := make(map[string]string)
	for _, cg := range vm.Cgroup {
		cgMap[cg.Path] = cg.Value
	}

	if cgMap["cpu.max"] != "10000 100000" {
		t.Errorf("cpu.max = %q, want %q", cgMap["cpu.max"], "10000 100000")
	}
	if cgMap["memory.high"] != "1073741824" {
		t.Errorf("memory.high = %q, want %q", cgMap["memory.high"], "1073741824")
	}
}

func TestExeletDesiredWithUserCgroupOverrides(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "useroverride@example.com")

	// Create a box
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "userbox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	containerID := "container-user-override"
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

	// Set user-level cgroup override
	userOverrides := "cpu.max:50000 100000"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.SetUserCgroupOverrides(ctx, exedb.SetUserCgroupOverridesParams{
			CgroupOverrides: &userOverrides,
			UserID:          userID,
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

	// The user-level override should appear on the group
	if len(ds.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(ds.Groups))
	}

	group := ds.Groups[0]
	if len(group.Cgroup) != 1 {
		t.Fatalf("expected 1 group cgroup setting, got %d: %v", len(group.Cgroup), group.Cgroup)
	}
	if group.Cgroup[0].Path != "cpu.max" || group.Cgroup[0].Value != "50000 100000" {
		t.Errorf("group cgroup = %v, want cpu.max:50000 100000", group.Cgroup[0])
	}

	// The VM should still have its own cpu.max from allocated_cpus
	if len(ds.VMs) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(ds.VMs))
	}
	if len(ds.VMs[0].Cgroup) != 1 {
		t.Fatalf("expected 1 VM cgroup setting, got %d", len(ds.VMs[0].Cgroup))
	}
	if ds.VMs[0].Cgroup[0].Path != "cpu.max" || ds.VMs[0].Cgroup[0].Value != "200000 100000" {
		t.Errorf("VM cgroup = %v, want cpu.max:200000 100000", ds.VMs[0].Cgroup[0])
	}
}

func TestExeletDesiredBoxOverrideRemovesCpuMax(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "removeoverride@example.com")

	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "removebox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	containerID := "container-remove-override"
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

	// Override that removes cpu.max (empty value) and adds memory.high
	overrides := "cpu.max:\nmemory.high:536870912"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.SetBoxCgroupOverrides(ctx, exedb.SetBoxCgroupOverridesParams{
			CgroupOverrides: &overrides,
			ID:              boxID,
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

	// cpu.max should be removed, only memory.high should remain
	vm := ds.VMs[0]
	if len(vm.Cgroup) != 1 {
		t.Fatalf("expected 1 cgroup setting, got %d: %v", len(vm.Cgroup), vm.Cgroup)
	}
	if vm.Cgroup[0].Path != "memory.high" || vm.Cgroup[0].Value != "536870912" {
		t.Errorf("cgroup = %v, want memory.high:536870912", vm.Cgroup[0])
	}
}

func TestExeletDesiredGroupDefaultUsesMaxCPUs(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "multivm@example.com")

	// Create two VMs: one with 2 CPUs, one with 4 CPUs.
	// The group default should use max(2,4) * 2 = 8 CPUs.
	for _, tc := range []struct {
		name string
		cpus uint64
		cid  string
	}{
		{"smallvm", 2, "container-small"},
		{"bigvm", 4, "container-big"},
	} {
		boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
			userID:        userID,
			ctrhost:       addr,
			name:          tc.name,
			image:         "ubuntu:latest",
			noShard:       true,
			region:        "pdx",
			allocatedCPUs: tc.cpus,
		})
		if err != nil {
			t.Fatal(err)
		}
		cid := tc.cid
		err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
				ID:          boxID,
				ContainerID: &cid,
				Status:      "running",
			})
		})
		if err != nil {
			t.Fatal(err)
		}
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

	if len(ds.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(ds.Groups))
	}

	// Group cpu.max should be 2 * max(2,4) = 8 CPUs = 800000 100000
	group := ds.Groups[0]
	if len(group.Cgroup) != 1 {
		t.Fatalf("expected 1 group cgroup, got %d: %v", len(group.Cgroup), group.Cgroup)
	}
	if group.Cgroup[0].Path != "cpu.max" || group.Cgroup[0].Value != "800000 100000" {
		t.Errorf("group cgroup = %v, want cpu.max:800000 100000", group.Cgroup[0])
	}

	// Both VMs should have their own per-VM cpu.max
	if len(ds.VMs) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(ds.VMs))
	}
}

func TestExeletDesiredWithIOMaxOverrides(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	addr := "tcp://fake-host:9080"
	server.exeletClients[addr] = &exeletClient{addr: addr}

	userID := createTestUser(t, server, "iooverride@example.com")

	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "iobox",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	containerID := "container-io-123"
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

	// Set IO bandwidth override with ~ device placeholder.
	overrides := "io.max:~ rbps=10485760 wbps=52428800"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.SetBoxCgroupOverrides(ctx, exedb.SetBoxCgroupOverridesParams{
			CgroupOverrides: &overrides,
			ID:              boxID,
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
	// Should have cpu.max (from allocated_cpus) + io.max (with ~ placeholder) = 2 settings.
	if len(vm.Cgroup) != 2 {
		t.Fatalf("expected 2 cgroup settings, got %d: %v", len(vm.Cgroup), vm.Cgroup)
	}

	cgMap := make(map[string]string)
	for _, cg := range vm.Cgroup {
		cgMap[cg.Path] = cg.Value
	}

	if cgMap["cpu.max"] != "200000 100000" {
		t.Errorf("cpu.max = %q, want %q", cgMap["cpu.max"], "200000 100000")
	}
	if cgMap["io.max"] != "~ rbps=10485760 wbps=52428800" {
		t.Errorf("io.max = %q, want %q", cgMap["io.max"], "~ rbps=10485760 wbps=52428800")
	}
}
