package exeweb

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func makeGz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func TestServePrecompressed_GzipWhenAccepted(t *testing.T) {
	original := []byte("console.log('hello')")
	gzData := makeGz(t, original)

	fs := fstest.MapFS{
		"assets/index-abc123.js":    &fstest.MapFile{Data: original},
		"assets/index-abc123.js.gz": &fstest.MapFile{Data: gzData},
	}

	req := httptest.NewRequest("GET", "/assets/index-abc123.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	ServePrecompressed(rec, req, fs, "assets/index-abc123.js")

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("expected Content-Encoding: gzip")
	}
	if rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("unexpected Content-Type: %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("expected Vary: Accept-Encoding")
	}

	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	decompressed, _ := io.ReadAll(gr)
	if string(decompressed) != string(original) {
		t.Fatalf("got %q, want %q", decompressed, original)
	}
}

func TestServePrecompressed_NoGzipWhenNotAccepted(t *testing.T) {
	original := []byte("console.log('hello')")
	gzData := makeGz(t, original)

	fs := fstest.MapFS{
		"assets/index-abc123.js":    &fstest.MapFile{Data: original},
		"assets/index-abc123.js.gz": &fstest.MapFile{Data: gzData},
	}

	req := httptest.NewRequest("GET", "/assets/index-abc123.js", nil)
	// No Accept-Encoding
	rec := httptest.NewRecorder()
	ServePrecompressed(rec, req, fs, "assets/index-abc123.js")

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("should not serve gzip without Accept-Encoding")
	}
	if !bytes.Contains(rec.Body.Bytes(), original) {
		t.Fatal("expected original content")
	}
}

func TestServePrecompressed_FallbackWhenNoGzFile(t *testing.T) {
	original := []byte("console.log('hello')")

	fs := fstest.MapFS{
		"assets/index-abc123.js": &fstest.MapFile{Data: original},
		// No .gz sibling
	}

	req := httptest.NewRequest("GET", "/assets/index-abc123.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	ServePrecompressed(rec, req, fs, "assets/index-abc123.js")

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("should not serve gzip when .gz file doesn't exist")
	}
}

func TestServePrecompressed_SkipsNonCompressibleTypes(t *testing.T) {
	original := []byte("fake font data")

	fs := fstest.MapFS{
		"assets/font-abc.woff2": &fstest.MapFile{Data: original},
	}

	req := httptest.NewRequest("GET", "/assets/font-abc.woff2", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	ServePrecompressed(rec, req, fs, "assets/font-abc.woff2")

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("should not attempt gzip for woff2")
	}
}

func TestServePrecompressed_CSS(t *testing.T) {
	original := []byte("body { color: red; }")
	gzData := makeGz(t, original)

	fs := fstest.MapFS{
		"assets/style-abc.css":    &fstest.MapFile{Data: original},
		"assets/style-abc.css.gz": &fstest.MapFile{Data: gzData},
	}

	req := httptest.NewRequest("GET", "/assets/style-abc.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	ServePrecompressed(rec, req, fs, "assets/style-abc.css")

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("expected gzip for CSS")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("unexpected Content-Type: %s", ct)
	}
}

func TestServePrecompressed_404(t *testing.T) {
	fs := fstest.MapFS{}

	req := httptest.NewRequest("GET", "/assets/nope.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	ServePrecompressed(rec, req, fs, "assets/nope.js")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", rec.Code)
	}
}
