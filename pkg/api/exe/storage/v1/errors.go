package v1

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	// ErrNotFound is returned when a resource is not found
	ErrNotFound = errors.New("not found")
	// ErrResourceExists is returned when an existing resource exists
	ErrResourceExists = errors.New("resource already exists")
)

// IsResourceExists is a helper that checks if the error is an already exists
func IsResourceExists(err error) bool {
	if errors.Is(err, ErrResourceExists) {
		return true
	}
	// check grpc error
	if s, ok := status.FromError(err); ok {
		if s.Code() == codes.AlreadyExists {
			return true
		}
	}
	return false
}
