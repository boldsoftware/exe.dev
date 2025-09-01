package container

import (
	"testing"
)

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"0.0 B", 0},
		{"403.0 B", 403},
		{"1.5 KiB", 1536},
		{"2.0 KB", 2048},
		{"16.2 MiB", 16986931},  // 16.2 * 1024 * 1024
		{"1.7 MB", 1782579},     // 1.7 * 1024 * 1024
		{"2.5 GiB", 2684354560}, // 2.5 * 1024 * 1024 * 1024
		{"1.0 GB", 1073741824},  // 1.0 * 1024 * 1024 * 1024
		{"", 0},
		{"invalid", 0},
		{"  100.0 B  ", 100},
	}

	for _, test := range tests {
		result := parseByteSize(test.input)
		if result != test.expected {
			t.Errorf("parseByteSize(%q) = %d, expected %d", test.input, result, test.expected)
		}
	}
}

func TestParsePullLine(t *testing.T) {
	progress := &pullProgress{
		layers: make(map[string]*layerProgress),
	}

	// Test parsing a downloading line
	line1 := "layer-sha256:021cb5923c0e90937539b3bc922d668109e6fa19b27d1e67bf0d6cb84cbc94d8: downloading    |------| 100.5 KiB/16.2 MiB"
	parsePullLine(line1, progress)

	layer1 := progress.layers["021cb5923c0e"]
	if layer1 == nil {
		t.Fatal("Expected layer to be tracked")
	}
	if layer1.status != "downloading" {
		t.Errorf("Expected status 'downloading', got %s", layer1.status)
	}
	if layer1.current != 102912 { // 100.5 * 1024
		t.Errorf("Expected current to be 102912, got %d", layer1.current)
	}
	if layer1.total != 16986931 { // 16.2 * 1024 * 1024
		t.Errorf("Expected total to be 16986931, got %d", layer1.total)
	}

	// Test parsing a done line
	line2 := "layer-sha256:021cb5923c0e90937539b3bc922d668109e6fa19b27d1e67bf0d6cb84cbc94d8: done"
	parsePullLine(line2, progress)

	if layer1.status != "done" {
		t.Errorf("Expected status 'done', got %s", layer1.status)
	}
	if layer1.current != layer1.total {
		t.Errorf("Expected current to equal total when done")
	}

	// Test with ANSI escape codes
	line3 := "\x1b[32mlayer-sha256:49f3b06c840fcb4c48cf9bfe1da039269b88c682942434e2bf8b266d3acdd4fd:\x1b[0m downloading    |\x1b[32m------\x1b[0m| 512.0 B/1.7 MiB"
	parsePullLine(line3, progress)

	layer2 := progress.layers["49f3b06c840f"]
	if layer2 == nil {
		t.Fatal("Expected layer to be tracked")
	}
	if layer2.current != 512 {
		t.Errorf("Expected current to be 512, got %d", layer2.current)
	}
	if layer2.total != 1782579 { // 1.7 * 1024 * 1024
		t.Errorf("Expected total to be 1782579, got %d", layer2.total)
	}

	// Test index-sha256 format (not just layer-sha256)
	line4 := "index-sha256:8feb4d8ca5354def3d8fce243717141ce31e2c428701f6682bd2fafe15388214: downloading    |------| 0.0 B/6.5 KiB"
	parsePullLine(line4, progress)

	indexLayer := progress.layers["8feb4d8ca535"]
	if indexLayer == nil {
		t.Fatal("Expected index layer to be tracked")
	}
	if indexLayer.total != 6656 { // 6.5 * 1024
		t.Errorf("Expected total to be 6656, got %d", indexLayer.total)
	}

	// Test manifest-sha256 format
	line5 := "manifest-sha256:ffa6ff1084ab549bb55a8d6d0d79709f1d1b1e622d0fa267c6aa30de17b26817: downloading    |------| 1.0 KiB/3.5 KiB"
	parsePullLine(line5, progress)

	manifestLayer := progress.layers["ffa6ff1084ab"]
	if manifestLayer == nil {
		t.Fatal("Expected manifest layer to be tracked")
	}
	if manifestLayer.current != 1024 {
		t.Errorf("Expected current to be 1024, got %d", manifestLayer.current)
	}
	if manifestLayer.total != 3584 { // 3.5 * 1024
		t.Errorf("Expected total to be 3584, got %d", manifestLayer.total)
	}
}

func TestPullProgressCalculation(t *testing.T) {
	progress := &pullProgress{
		layers: make(map[string]*layerProgress),
	}

	// Add some layers
	progress.layers["layer1"] = &layerProgress{
		status:  "done",
		current: 1000,
		total:   1000,
	}
	progress.layers["layer2"] = &layerProgress{
		status:  "downloading",
		current: 500,
		total:   2000,
	}
	progress.layers["layer3"] = &layerProgress{
		status:  "waiting",
		current: 0,
		total:   3000,
	}

	// Parse a line to trigger recalculation
	line := "layer-sha256:abcdef123456789012345678901234567890123456789012345678901234: downloading    |------| 250.0 B/1.0 KiB"
	parsePullLine(line, progress)

	expectedTotal := int64(1000 + 2000 + 3000 + 1024) // All layer totals
	expectedDownloaded := int64(1000 + 500 + 250)     // done + downloading progress

	if progress.totalBytes != expectedTotal {
		t.Errorf("Expected total bytes %d, got %d", expectedTotal, progress.totalBytes)
	}
	if progress.downloadedBytes != expectedDownloaded {
		t.Errorf("Expected downloaded bytes %d, got %d", expectedDownloaded, progress.downloadedBytes)
	}
}

func TestProgressCallbackCompatibility(t *testing.T) {
	// Test that both old and new callbacks work
	req := &CreateContainerRequest{}

	// Test with old callback
	var oldCalled bool
	var oldPhase CreateProgress
	var oldImageBytes int64

	req.ProgressCallback = func(phase CreateProgress, imageBytes int64) {
		oldCalled = true
		oldPhase = phase
		oldImageBytes = imageBytes
	}

	reportProgress(req, CreatePull, 1000, 500, "test")

	if !oldCalled {
		t.Error("Old callback was not called")
	}
	if oldPhase != CreatePull {
		t.Errorf("Expected phase CreatePull, got %v", oldPhase)
	}
	if oldImageBytes != 1000 {
		t.Errorf("Expected imageBytes 1000, got %d", oldImageBytes)
	}

	// Test with new callback
	req.ProgressCallback = nil
	var newCalled bool
	var newInfo CreateProgressInfo

	req.ProgressCallbackEx = func(info CreateProgressInfo) {
		newCalled = true
		newInfo = info
	}

	reportProgress(req, CreateStart, 2000, 1500, "Starting")

	if !newCalled {
		t.Error("New callback was not called")
	}
	if newInfo.Phase != CreateStart {
		t.Errorf("Expected phase CreateStart, got %v", newInfo.Phase)
	}
	if newInfo.ImageBytes != 2000 {
		t.Errorf("Expected imageBytes 2000, got %d", newInfo.ImageBytes)
	}
	if newInfo.DownloadedBytes != 1500 {
		t.Errorf("Expected downloadedBytes 1500, got %d", newInfo.DownloadedBytes)
	}
	if newInfo.Message != "Starting" {
		t.Errorf("Expected message 'Starting', got %s", newInfo.Message)
	}

	// Test with both callbacks (new should take precedence)
	req.ProgressCallback = func(phase CreateProgress, imageBytes int64) {
		oldCalled = true
	}
	req.ProgressCallbackEx = func(info CreateProgressInfo) {
		newCalled = true
	}

	oldCalled = false
	newCalled = false
	reportProgress(req, CreateDone, 3000, 3000, "Done")

	if oldCalled {
		t.Error("Old callback should not be called when new callback is present")
	}
	if !newCalled {
		t.Error("New callback should be called")
	}
}
