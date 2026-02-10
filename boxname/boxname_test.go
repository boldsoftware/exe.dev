package boxname

import (
	"fmt"
	"testing"
)

func TestBoxWordsHaveNoDuplicates(t *testing.T) {
	t.Parallel()
	wordSet := make(map[string]bool)
	for _, word := range words {
		if wordSet[word] {
			t.Errorf("duplicate word found in boxname words: %q", word)
		}
		wordSet[word] = true
	}
}

func TestGeneratedBoxNamesAreValid(t *testing.T) {
	t.Parallel()
	for range 10 {
		name := Random()
		if !IsValid(name) {
			t.Errorf("Generated name %q is not valid", name)
		}
		if len(name) > 30 {
			t.Errorf("Generated name %q is too long (%d chars)", name, len(name))
		}
	}
}

func TestReservedPortSuffixes(t *testing.T) {
	t.Parallel()

	// Names ending with -NNN (hyphen + digits only) should be invalid
	invalidNames := []string{
		"mybox-123",
		"mybox-1",
		"mybox-80",
		"mybox-8080",
		"foo-bar-443",
	}
	for _, name := range invalidNames {
		if IsValid(name) {
			t.Errorf("Name with reserved port suffix %q should be invalid", name)
		}
	}

	// Names ending with -pNNN (hyphen + p + digits only) should be invalid
	invalidPNames := []string{
		"mybox-p80",
		"mybox-p8080",
		"mybox-p1",
		"foo-bar-p443",
	}
	for _, name := range invalidPNames {
		if IsValid(name) {
			t.Errorf("Name with reserved port suffix %q should be invalid", name)
		}
	}

	// Names ending with -portNNN should be invalid
	invalidPortNames := []string{
		"mybox-port80",
		"mybox-port8080",
		"mybox-port1",
		"foo-bar-port443",
	}
	for _, name := range invalidPortNames {
		if IsValid(name) {
			t.Errorf("Name with reserved port suffix %q should be invalid", name)
		}
	}

	// Names matching ^(p|port)[0-9]*$ should be invalid
	invalidPOnlyNames := []string{
		"p8080",
		"p80800",
		"p808000",
		"port80",
		"port8080",
		"port80800",
	}
	for _, name := range invalidPOnlyNames {
		if IsValid(name) {
			t.Errorf("Name %q should be invalid (reserved port pattern)", name)
		}
	}

	// Names that look similar but are valid should still be valid
	validNames := []string{
		"mybox-a123",    // has letter before digits
		"mybox-123a",    // has letter after digits
		"mybox-p80a",    // has letter after digits
		"mybox-ap80",    // has 'a' before 'p'
		"mybox-port80a", // has letter after digits
		"mybox-aport80", // has 'a' before 'port'
	}
	for _, name := range validNames {
		if !IsValid(name) {
			t.Errorf("Name %q should be valid", name)
		}
	}
}

func TestShelleyReserved(t *testing.T) {
	t.Parallel()
	if IsValid("shelley") {
		t.Error("'shelley' should be reserved")
	}
}

func TestTeamRenameNameAlwaysValid(t *testing.T) {
	t.Parallel()
	// The teams_test.go rename subtest constructs names like:
	//   "renamed-" + memberBox[:8] + "-vm"
	// where memberBox[:8] is "e1e-XXXX" and XXXX is a 4-digit hex testRunID.
	// Verify this pattern produces valid names for all possible testRunIDs.
	for i := range 65536 {
		id := fmt.Sprintf("%04x", i)
		name := "renamed-e1e-" + id + "-vm"
		if err := Valid(name); err != nil {
			t.Errorf("rename target %q is invalid for testRunID=%s: %v", name, id, err)
		}
	}
}

func TestDrugSpamDenylist(t *testing.T) {
	t.Parallel()
	for _, name := range denySubstrings {
		if IsValid(name) {
			t.Errorf("Denylisted drug spam name %q is considered valid", name)
		}
		if IsValid(name+"box") || IsValid("my"+name) || IsValid("my-"+name+"-box") {
			t.Errorf("Name containing denylisted drug spam %q is considered valid", name)
		}
	}

	// Explicitly test: "adderall"
	if IsValid("adderall") {
		t.Error("Denylisted drug spam name 'adderall' is considered valid")
	}
	if IsValid("adderallbox") {
		t.Error("Name containing denylisted drug spam 'adderall' is considered valid")
	}
	if IsValid("allOfTheAdderallRightNow") {
		t.Error("Name containing denylisted drug spam 'adderall' is considered valid")
	}
}
