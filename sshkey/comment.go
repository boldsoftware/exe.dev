package sshkey

import (
	"fmt"
	"regexp"
	"strings"
)

// shellMetachars matches shell metacharacters, path separators, and control characters.
// Includes: | & ; $ ` \ " ' < > ( ) { } [ ] ! # * ? ~ / and ASCII control chars (0x00-0x1f)
var shellMetachars = regexp.MustCompile(`[|&;$` + "`" + `\\"'<>(){}\[\]!#*?~/\x00-\x1f]`)

// SanitizeComment sanitizes an SSH key comment/name:
// - Removes leading dashes (to prevent flag confusion)
// - Removes shell metacharacters
// - Collapses/removes spaces
// - Truncates to 64 characters
func SanitizeComment(comment string) string {
	// Remove shell metacharacters
	comment = shellMetachars.ReplaceAllString(comment, "")

	// Collapse multiple spaces and trim
	comment = strings.Join(strings.Fields(comment), "-")

	// Remove leading dashes
	comment = strings.TrimLeft(comment, "-")

	// Truncate to 64 characters
	comment = comment[:min(64, len(comment))]

	return comment
}

// GeneratedComment returns the auto-generated comment for a key number.
func GeneratedComment[T ~int | ~int64](keyNumber T) string {
	return fmt.Sprintf("key-%d", keyNumber)
}
