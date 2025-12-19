package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/loop"
)

func TestUploadEndpoint(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create a multipart form with a file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create a test file
	part, err := writer.CreateFormFile("file", "test.png")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}

	// Write some fake PNG content (just the magic header bytes)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if _, err := part.Write(pngData); err != nil {
		t.Fatalf("failed to write file content: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	server.handleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	path, ok := response["path"]
	if !ok {
		t.Fatal("response missing 'path' field")
	}

	// Verify the path is in the screenshot directory
	if !strings.HasPrefix(path, browse.ScreenshotDir) {
		t.Errorf("expected path to start with %s, got %s", browse.ScreenshotDir, path)
	}

	// Verify the file has the correct extension
	if !strings.HasSuffix(path, ".png") {
		t.Errorf("expected path to end with .png, got %s", path)
	}

	// Verify the file exists and contains our data
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}

	if !bytes.Equal(data, pngData) {
		t.Errorf("uploaded file content mismatch")
	}

	// Clean up uploaded file
	os.Remove(path)
}

func TestUploadEndpointMethodNotAllowed(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	req := httptest.NewRequest("GET", "/api/upload", nil)
	w := httptest.NewRecorder()

	server.handleUpload(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", w.Code)
	}
}

func TestUploadEndpointNoFile(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create an empty multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	server.handleUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUploadedFileCanBeReadViaReadEndpoint(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// First, upload a file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", "test.jpg")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}

	// Write some fake JPEG content
	jpgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	if _, err := part.Write(jpgData); err != nil {
		t.Fatalf("failed to write file content: %v", err)
	}
	writer.Close()

	uploadReq := httptest.NewRequest("POST", "/api/upload", body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadW := httptest.NewRecorder()

	server.handleUpload(uploadW, uploadReq)

	if uploadW.Code != http.StatusOK {
		t.Fatalf("upload failed: %s", uploadW.Body.String())
	}

	var uploadResponse map[string]string
	if err := json.Unmarshal(uploadW.Body.Bytes(), &uploadResponse); err != nil {
		t.Fatalf("failed to parse upload response: %v", err)
	}

	path := uploadResponse["path"]

	// Now try to read the file via the read endpoint
	readReq := httptest.NewRequest("GET", "/api/read?path="+path, nil)
	readW := httptest.NewRecorder()

	server.handleRead(readW, readReq)

	if readW.Code != http.StatusOK {
		t.Fatalf("read failed with status %d: %s", readW.Code, readW.Body.String())
	}

	// Verify content type
	contentType := readW.Header().Get("Content-Type")
	if contentType != "image/jpeg" {
		t.Errorf("expected Content-Type image/jpeg, got %s", contentType)
	}

	// Verify content
	readData, err := io.ReadAll(readW.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if !bytes.Equal(readData, jpgData) {
		t.Errorf("read content mismatch")
	}

	// Clean up
	os.Remove(path)
}

func TestUploadPreservesFileExtension(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	testCases := []struct {
		filename string
		wantExt  string
	}{
		{"photo.png", ".png"},
		{"image.jpeg", ".jpeg"},
		{"screenshot.gif", ".gif"},
		{"document.pdf", ".pdf"},
		{"noextension", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)

			part, err := writer.CreateFormFile("file", tc.filename)
			if err != nil {
				t.Fatalf("failed to create form file: %v", err)
			}
			part.Write([]byte("test content"))
			writer.Close()

			req := httptest.NewRequest("POST", "/api/upload", body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			w := httptest.NewRecorder()

			server.handleUpload(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", w.Code)
			}

			var response map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			path := response["path"]
			ext := filepath.Ext(path)
			if ext != tc.wantExt {
				t.Errorf("expected extension %q, got %q", tc.wantExt, ext)
			}

			// Clean up
			os.Remove(path)
		})
	}
}
