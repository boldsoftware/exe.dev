package utils

import (
	"crypto/sha1"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a resource is not found
var ErrNotFound = errors.New("not found")

// GetID returns an addressable ID using the specified parameters.
func GetID(v ...string) string {
	h := sha1.New()
	for _, x := range v {
		h.Write([]byte(x))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Truncate returns a truncated string of a default length
func Truncate(v string) string {
	maxLen := 12
	if len(v) > maxLen {
		return v[:maxLen]
	}
	return v
}
