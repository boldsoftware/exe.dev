package replication

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrTargetFull indicates the replication target ran out of space. Wrapping
// errors with this sentinel lets the worker skip retries and skip the
// destroy-and-full-fallback path so an existing remote backup is not lost
// chasing a send that cannot succeed.
var ErrTargetFull = errors.New("replication target out of space")

// permanentErrorPhrases are substrings observed in zfs recv stderr (and
// equivalents for file/zpool targets) that indicate a send will never succeed
// without operator intervention.
var permanentErrorPhrases = []string{
	"out of space",
	"no space left on device",
	"dataset is full",
	"quota exceeded",
}

// isOutOfSpaceErr reports whether the given stderr or error message contains
// a known out-of-space marker. Matching is case-insensitive.
func isOutOfSpaceErr(messages ...string) bool {
	for _, m := range messages {
		if m == "" {
			continue
		}
		lower := strings.ToLower(m)
		for _, phrase := range permanentErrorPhrases {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
	}
	return false
}

// classifySendErr inspects stderr for a permanent-failure marker and, when
// found, wraps err so callers can detect it via errors.Is(err, ErrTargetFull).
// When no marker is present the original error is returned unchanged.
func classifySendErr(err error, stderr ...string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTargetFull) {
		return err
	}
	if isOutOfSpaceErr(append(stderr, err.Error())...) {
		return fmt.Errorf("%w: %w", ErrTargetFull, err)
	}
	return err
}

// availableSpaceCmd is the argument list passed to `zfs` to read the
// "available" property of a pool or dataset in raw bytes. Shared so every
// target issues the same query.
func availableSpaceCmd(pool string) []string {
	return []string{"get", "-Hp", "-o", "value", "available", pool}
}

// parseAvailableSpace parses the output of `zfs get -Hp -o value available`.
func parseAvailableSpace(pool string, out []byte) (uint64, error) {
	value := strings.TrimSpace(string(out))
	avail, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse available space for %s (%q): %w", pool, value, err)
	}
	return avail, nil
}
