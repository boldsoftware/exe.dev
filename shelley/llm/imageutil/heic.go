package imageutil

import (
	"bytes"
	"fmt"
	"os/exec"
)

// IsHEIC checks if data is a HEIC/HEIF image based on file magic.
// HEIC files are ISO Base Media File Format containers with specific brand codes.
func IsHEIC(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	// ftyp box starts at offset 4, brand at offset 8
	// Common brands: heic, heix, hevc, hevx, mif1, msf1
	if data[4] != 'f' || data[5] != 't' || data[6] != 'y' || data[7] != 'p' {
		return false
	}
	brand := string(data[8:12])
	switch brand {
	case "heic", "heix", "hevc", "hevx", "mif1", "msf1", "avif":
		return true
	}
	return false
}

// ConvertHEICToPNG converts HEIC image data to PNG using ImageMagick's convert command.
// Returns the PNG data or an error if conversion fails.
func ConvertHEICToPNG(data []byte) ([]byte, error) {
	cmd := exec.Command("convert", "heic:-", "png:-")
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("convert heic to png: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
