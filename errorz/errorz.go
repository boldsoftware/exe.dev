// Package errorz extends package errors.
package errorz

import "errors"

// HasType reports whether whether any error in err's tree has type T.
func HasType[T any](err error) bool {
	// https://pkg.go.dev/errors@master#AsType almost covers this...but can't be used in a conjunction.
	var target T
	return errors.As(err, &target)
}
