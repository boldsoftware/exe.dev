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

// GetTapName returns the tap interface name for the given VM ID.
func GetTapName(id string) string {
	return fmt.Sprintf("tap-%s", GetID(id)[:6])
}
