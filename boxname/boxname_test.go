package boxname

import "testing"

func TestGeneratedBoxNamesAreValid(t *testing.T) {
	t.Parallel()
	for range 10 {
		name := Random()
		if !Valid(name) {
			t.Errorf("Generated name '%s' is not valid", name)
		}
		if len(name) > 30 {
			t.Errorf("Generated name '%s' is too long (%d chars)", name, len(name))
		}
	}
}
