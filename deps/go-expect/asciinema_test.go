// Copyright 2018 Netflix, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package expect

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAsciinemaWriter(t *testing.T) {
	// Create temporary file
	file, err := ioutil.TempFile("", "asciinema_test_*.cast")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	// Create AsciinemaWriter
	var buf bytes.Buffer
	writer, err := NewAsciinemaWriterWithWriter(&buf, 80, 24, file)
	if err != nil {
		t.Fatalf("Failed to create AsciinemaWriter: %v", err)
	}
	defer writer.Close()

	// Write some events
	err = writer.WriteOutput("Hello World")
	if err != nil {
		t.Fatalf("Failed to write output: %v", err)
	}

	err = writer.WriteInput("ls")
	if err != nil {
		t.Fatalf("Failed to write input: %v", err)
	}

	err = writer.WriteResize(120, 30)
	if err != nil {
		t.Fatalf("Failed to write resize: %v", err)
	}

	err = writer.WriteMarker("test-marker")
	if err != nil {
		t.Fatalf("Failed to write marker: %v", err)
	}

	// Parse and verify output
	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	if len(lines) != 5 { // header + 4 events
		t.Fatalf("Expected 5 lines, got %d: %v", len(lines), lines)
	}

	// Verify header
	var header AsciinemaHeader
	err = json.Unmarshal([]byte(lines[0]), &header)
	if err != nil {
		t.Fatalf("Failed to parse header: %v", err)
	}

	if header.Version != 2 {
		t.Errorf("Expected version 2, got %d", header.Version)
	}
	if header.Width != 80 {
		t.Errorf("Expected width 80, got %d", header.Width)
	}
	if header.Height != 24 {
		t.Errorf("Expected height 24, got %d", header.Height)
	}

	// Verify events
	expectedEvents := []struct {
		eventType string
		data      string
	}{
		{"o", "Hello World"},
		{"i", "ls"},
		{"r", "120x30"},
		{"m", "test-marker"},
	}

	for i, expected := range expectedEvents {
		var event []interface{}
		err = json.Unmarshal([]byte(lines[i+1]), &event)
		if err != nil {
			t.Fatalf("Failed to parse event %d: %v", i+1, err)
		}

		if len(event) != 3 {
			t.Fatalf("Expected 3 elements in event, got %d", len(event))
		}

		// Check timestamp is a float64
		if _, ok := event[0].(float64); !ok {
			t.Errorf("Expected timestamp to be float64, got %T", event[0])
		}

		// Check event type
		if event[1] != expected.eventType {
			t.Errorf("Expected event type %s, got %v", expected.eventType, event[1])
		}

		// Check data
		if event[2] != expected.data {
			t.Errorf("Expected data %s, got %v", expected.data, event[2])
		}
	}
}

func TestConsoleWithAsciinemaRecording(t *testing.T) {
	// Create temporary file
	file, err := ioutil.TempFile("", "console_test_*.cast")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	filename := file.Name()
	file.Close()
	defer os.Remove(filename)

	// Create console with asciinema recording
	console, err := NewConsole(WithAsciinemaRecording(filename))
	if err != nil {
		t.Fatalf("Failed to create console: %v", err)
	}
	defer console.Close()

	// Verify recording is active
	if !console.IsRecording() {
		t.Error("Expected recording to be active")
	}

	// Send some data
	_, err = console.Send("test message")
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Give a moment for async operations
	time.Sleep(10 * time.Millisecond)

	// Stop recording
	err = console.StopRecording()
	if err != nil {
		t.Fatalf("Failed to stop recording: %v", err)
	}

	// Verify recording is stopped
	if console.IsRecording() {
		t.Error("Expected recording to be stopped")
	}

	// Read and verify the recorded file
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatalf("Failed to read recording file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 1 {
		t.Fatal("Expected at least header line in recording")
	}

	// Verify header
	var header AsciinemaHeader
	err = json.Unmarshal([]byte(lines[0]), &header)
	if err != nil {
		t.Fatalf("Failed to parse header: %v", err)
	}

	if header.Version != 2 {
		t.Errorf("Expected version 2, got %d", header.Version)
	}
}

func TestConsoleProgrammaticRecording(t *testing.T) {
	// Create console without initial recording
	console, err := NewConsole()
	if err != nil {
		t.Fatalf("Failed to create console: %v", err)
	}
	defer console.Close()

	// Verify no recording initially
	if console.IsRecording() {
		t.Error("Expected no recording initially")
	}

	// Create temporary file
	file, err := ioutil.TempFile("", "programmatic_test_*.cast")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	filename := file.Name()
	file.Close()
	defer os.Remove(filename)

	// Start recording programmatically
	err = console.StartRecording(filename)
	if err != nil {
		t.Fatalf("Failed to start recording: %v", err)
	}

	// Verify recording is active
	if !console.IsRecording() {
		t.Error("Expected recording to be active")
	}

	// Send some data
	_, err = console.Send("programmatic test")
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Give a moment for async operations
	time.Sleep(10 * time.Millisecond)

	// Stop recording
	err = console.StopRecording()
	if err != nil {
		t.Fatalf("Failed to stop recording: %v", err)
	}

	// Verify recording is stopped
	if console.IsRecording() {
		t.Error("Expected recording to be stopped")
	}

	// Verify file was created and has content
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatalf("Failed to read recording file: %v", err)
	}

	if len(content) == 0 {
		t.Error("Expected recording file to have content")
	}
}

func TestAsciinemaWriterClose(t *testing.T) {
	var buf bytes.Buffer
	writer, err := NewAsciinemaWriterWithWriter(&buf, 80, 24, nil)
	if err != nil {
		t.Fatalf("Failed to create AsciinemaWriter: %v", err)
	}

	// Write an event
	err = writer.WriteOutput("test")
	if err != nil {
		t.Fatalf("Failed to write output: %v", err)
	}

	// Close writer
	err = writer.Close()
	if err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	// Try to write after close (should fail)
	err = writer.WriteOutput("should fail")
	if err == nil {
		t.Error("Expected error when writing to closed writer")
	}

	// Multiple closes should be safe
	err = writer.Close()
	if err != nil {
		t.Fatalf("Failed to close writer second time: %v", err)
	}
}