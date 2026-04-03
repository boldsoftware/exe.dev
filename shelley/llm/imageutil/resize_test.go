package imageutil

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func createTestPNG(t *testing.T, width, height int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	// Fill with a solid color using direct pixel buffer access (much faster than per-pixel Set).
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = 100
		pix[i+1] = 150
		pix[i+2] = 200
		pix[i+3] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("Failed to create test image: %v", err)
	}
	return buf.Bytes()
}

func TestResizeImage(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		maxDim     int
		wantResize bool
		wantMaxDim int
	}{
		{"small image", 80, 60, 200, false, 80},
		{"at limit", 200, 200, 200, false, 200},
		{"width exceeds", 300, 100, 200, true, 200},
		{"height exceeds", 100, 300, 200, true, 200},
		{"both exceed", 300, 300, 200, true, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := createTestPNG(t, tt.width, tt.height)
			resized, format, didResize, err := ResizeImage(data, tt.maxDim)
			if err != nil {
				t.Fatalf("ResizeImage() error = %v", err)
			}
			if didResize != tt.wantResize {
				t.Errorf("ResizeImage() didResize = %v, want %v", didResize, tt.wantResize)
			}
			if format != "png" {
				t.Errorf("ResizeImage() format = %v, want png", format)
			}
			if didResize {
				// Verify the resized image dimensions
				config, _, err := image.DecodeConfig(bytes.NewReader(resized))
				if err != nil {
					t.Fatalf("Failed to decode resized image: %v", err)
				}
				if config.Width > tt.maxDim || config.Height > tt.maxDim {
					t.Errorf("Resized image %dx%d still exceeds max %d", config.Width, config.Height, tt.maxDim)
				}
			} else {
				if !bytes.Equal(resized, data) {
					t.Error("Expected original data when no resize needed")
				}
			}
		})
	}
}

func TestResizeImageJPEG(t *testing.T) {
	// Create a test JPEG image
	img := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = 100
		pix[i+1] = 150
		pix[i+2] = 200
		pix[i+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("Failed to create test JPEG image: %v", err)
	}
	data := buf.Bytes()

	resized, format, didResize, err := ResizeImage(data, 200)
	if err != nil {
		t.Fatalf("ResizeImage() error = %v", err)
	}
	if !didResize {
		t.Error("Expected resize for large JPEG image")
	}
	if format != "jpeg" {
		t.Errorf("ResizeImage() format = %v, want jpeg", format)
	}

	// Verify the resized image dimensions
	config, _, err := image.DecodeConfig(bytes.NewReader(resized))
	if err != nil {
		t.Fatalf("Failed to decode resized image: %v", err)
	}
	if config.Width > 200 || config.Height > 200 {
		t.Errorf("Resized image %dx%d still exceeds max 200", config.Width, config.Height)
	}
}

func TestResizeImageErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		maxDim  int
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []byte{},
			maxDim:  2000,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ResizeImage(tt.data, tt.maxDim)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResizeImage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResizeImageNoResizeNeeded(t *testing.T) {
	data := createTestPNG(t, 80, 60)
	resized, format, didResize, err := ResizeImage(data, 200)
	if err != nil {
		t.Fatalf("ResizeImage() error = %v", err)
	}
	if didResize {
		t.Error("Expected no resize for small image")
	}
	if format != "png" {
		t.Errorf("ResizeImage() format = %v, want png", format)
	}
	if !bytes.Equal(resized, data) {
		t.Error("Expected original data when no resize needed")
	}
}
