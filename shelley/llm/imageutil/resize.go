// Package imageutil provides image manipulation utilities.
package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	"golang.org/x/image/draw"
)

// ResizeImage resizes an image if any dimension exceeds maxDimension.
// Returns the resized image bytes and the format ("png" or "jpeg").
// If no resize is needed, returns the original data unchanged.
func ResizeImage(data []byte, maxDimension int) (resized []byte, format string, didResize bool, err error) {
	img, detectedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width <= maxDimension && height <= maxDimension {
		return data, detectedFormat, false, nil
	}

	// Calculate new dimensions preserving aspect ratio
	newWidth, newHeight := width, height
	if width > height {
		newWidth = maxDimension
		newHeight = height * maxDimension / width
	} else {
		newHeight = maxDimension
		newWidth = width * maxDimension / height
	}

	// Create resized image
	resizedImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.BiLinear.Scale(resizedImg, resizedImg.Bounds(), img, bounds, draw.Over, nil)

	// Encode to the same format
	var buf bytes.Buffer
	switch strings.ToLower(detectedFormat) {
	case "jpeg", "jpg":
		err = jpeg.Encode(&buf, resizedImg, &jpeg.Options{Quality: 85})
		format = "jpeg"
	default:
		err = png.Encode(&buf, resizedImg)
		format = "png"
	}

	if err != nil {
		return nil, "", false, fmt.Errorf("failed to encode resized image: %w", err)
	}

	return buf.Bytes(), format, true, nil
}
