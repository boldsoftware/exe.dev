// Package errorz extends package errors.
package errorz

import "errors"

// HasType reports whether whether any error in err's tree has type T.
func HasType[T any](err error) bool {
	// https://pkg.go.dev/errors@master#AsType almost covers this...but can't be used in a conjunction.
	var target T
	return errors.As(err, &target)
}

// AsType is https://pkg.go.dev/errors@master#AsType, backported from Go 1.26.
// When we upgrade to Go 1.26, we can delete this and use errors.AsType directly.
func AsType[E error](err error) (E, bool) {
	if err == nil {
		var zero E
		return zero, false
	}
	var pe *E // lazily initialized
	return asType(err, &pe)
}

func asType[E error](err error, ppe **E) (_ E, _ bool) {
	for {
		if e, ok := err.(E); ok {
			return e, true
		}
		if x, ok := err.(interface{ As(any) bool }); ok {
			if *ppe == nil {
				*ppe = new(E)
			}
			if x.As(*ppe) {
				return **ppe, true
			}
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
			if err == nil {
				return
			}
		case interface{ Unwrap() []error }:
			for _, err := range x.Unwrap() {
				if err == nil {
					continue
				}
				if x, ok := asType(err, ppe); ok {
					return x, true
				}
			}
			return
		default:
			return
		}
	}
}
