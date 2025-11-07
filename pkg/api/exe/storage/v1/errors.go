package v1

import "errors"

var (
	// ErrNotFound is returned when a resource is not found
	ErrNotFound = errors.New("not found")
	// ErrResourceExists is returned when an existing resource exists
	ErrResourceExists = errors.New("resource already exists")
)
