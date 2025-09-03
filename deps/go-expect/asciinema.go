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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// AsciinemaWriter handles ASCIIcast v2 format recording
type AsciinemaWriter struct {
	writer    io.Writer
	file      *os.File
	startTime time.Time
	mutex     sync.Mutex
	closed    bool
}

// AsciinemaHeader represents the ASCIIcast v2 header
type AsciinemaHeader struct {
	Version   int      `json:"version"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Timestamp *int64   `json:"timestamp,omitempty"`
	Duration  *float64 `json:"duration,omitempty"`
	Command   *string  `json:"command,omitempty"`
	Title     *string  `json:"title,omitempty"`
}

// AsciinemaEvent represents a single event in ASCIIcast v2 format
type AsciinemaEvent struct {
	Time float64
	Code string
	Data string
}

// NewAsciinemaWriter creates a new ASCIIcinema recorder writing to the specified file
func NewAsciinemaWriter(filename string, width, height int) (*AsciinemaWriter, error) {
	file, err := os.Create(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create asciinema file: %w", err)
	}

	return NewAsciinemaWriterWithWriter(file, width, height, file)
}

// NewAsciinemaWriterWithWriter creates a new ASCIIcinema recorder writing to the specified writer
func NewAsciinemaWriterWithWriter(writer io.Writer, width, height int, closer io.Closer) (*AsciinemaWriter, error) {
	aw := &AsciinemaWriter{
		writer:    writer,
		startTime: time.Now(),
	}

	if f, ok := closer.(*os.File); ok {
		aw.file = f
	}

	// Write header
	timestamp := aw.startTime.Unix()
	header := AsciinemaHeader{
		Version:   2,
		Width:     width,
		Height:    height,
		Timestamp: &timestamp,
	}

	headerBytes, err := json.Marshal(header)
	if err != nil {
		if aw.file != nil {
			aw.file.Close()
		}
		return nil, fmt.Errorf("failed to marshal header: %w", err)
	}

	_, err = fmt.Fprintf(aw.writer, "%s\n", headerBytes)
	if err != nil {
		if aw.file != nil {
			aw.file.Close()
		}
		return nil, fmt.Errorf("failed to write header: %w", err)
	}

	return aw, nil
}

// WriteEvent writes a single event to the ASCIIcast recording
func (aw *AsciinemaWriter) WriteEvent(eventType, data string) error {
	aw.mutex.Lock()
	defer aw.mutex.Unlock()

	if aw.closed {
		return fmt.Errorf("asciinema writer is closed")
	}

	elapsed := time.Since(aw.startTime).Seconds()

	event := []interface{}{elapsed, eventType, data}
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	_, err = fmt.Fprintf(aw.writer, "%s\n", eventBytes)
	if err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}

	return nil
}

// WriteOutput writes terminal output data
func (aw *AsciinemaWriter) WriteOutput(data string) error {
	return aw.WriteEvent("o", data)
}

// WriteInput writes terminal input data
func (aw *AsciinemaWriter) WriteInput(data string) error {
	return aw.WriteEvent("i", data)
}

// WriteResize writes terminal resize event
func (aw *AsciinemaWriter) WriteResize(width, height int) error {
	resizeData := fmt.Sprintf("%dx%d", width, height)
	return aw.WriteEvent("r", resizeData)
}

// WriteMarker writes a marker event
func (aw *AsciinemaWriter) WriteMarker(name string) error {
	return aw.WriteEvent("m", name)
}

// Close closes the ASCIIcinema writer
func (aw *AsciinemaWriter) Close() error {
	aw.mutex.Lock()
	defer aw.mutex.Unlock()

	if aw.closed {
		return nil
	}

	aw.closed = true

	if aw.file != nil {
		return aw.file.Close()
	}

	return nil
}

// asciinemaOutputWriter is a wrapper that implements io.Writer and captures
// all terminal output for ASCIIcinema recording
type asciinemaOutputWriter struct {
	asciinemaWriter *AsciinemaWriter
}

// Write implements io.Writer and records terminal output
func (w *asciinemaOutputWriter) Write(p []byte) (n int, err error) {
	if w.asciinemaWriter != nil && len(p) > 0 {
		w.asciinemaWriter.WriteOutput(string(p))
	}
	return len(p), nil
}
