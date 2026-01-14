package v1

import "errors"

var (
	// ErrNotFound is returned when a resource is not found
	ErrNotFound = errors.New("not found")
	// ErrResourceExists is returned when an existing resource exists
	ErrResourceExists = errors.New("resource already exists")
	// ErrMigrating is returned when an operation is attempted on an instance that is being migrated
	ErrMigrating = errors.New("instance is being migrated")
)
