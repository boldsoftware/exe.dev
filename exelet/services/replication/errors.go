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

// queryRemoteAvailableSpace runs `zfs get -Hp -o value available <pool>` via
// the supplied runner and parses the result. Used by SSH/system-SSH/zpool
// targets so they share a single parser.
func queryRemoteAvailableSpace(pool string, run func(cmd string) ([]byte, error)) (uint64, error) {
	out, err := run(fmt.Sprintf("zfs get -Hp -o value available %s", pool))
	if err != nil {
		return 0, fmt.Errorf("query available space for %s: %w (output: %s)", pool, err, strings.TrimSpace(string(out)))
	}
	value := strings.TrimSpace(string(out))
	avail, parseErr := strconv.ParseUint(value, 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("parse available space %q: %w", value, parseErr)
	}
	return avail, nil
}
