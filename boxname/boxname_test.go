package boxname

import "testing"

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
		if !Valid(name) {
			t.Errorf("Generated name '%s' is not valid", name)
		}
		if len(name) > 30 {
			t.Errorf("Generated name '%s' is too long (%d chars)", name, len(name))
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
		if Valid(name) {
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
		if Valid(name) {
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
		if Valid(name) {
			t.Errorf("Name with reserved port suffix %q should be invalid", name)
		}
	}

	// Names matching ^p[0-9]*$ should be invalid
	invalidPOnlyNames := []string{
		"p8080",
		"p80800",
		"p808000",
	}
	for _, name := range invalidPOnlyNames {
		if Valid(name) {
			t.Errorf("Name %q should be invalid (reserved p+digits pattern)", name)
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
		if !Valid(name) {
			t.Errorf("Name %q should be valid", name)
		}
	}
}

func TestShelleyReserved(t *testing.T) {
	t.Parallel()
	if Valid("shelley") {
		t.Error("'shelley' should be reserved and thus invalid")
	}
}

func TestDrugSpamDenylist(t *testing.T) {
	t.Parallel()
	for _, name := range denySubstrings {
		if Valid(name) {
			t.Errorf("Denylisted drug spam name '%s' is considered valid", name)
		}
		if Valid(name+"box") || Valid("my"+name) || Valid("my-"+name+"-box") {
			t.Errorf("Name containing denylisted drug spam '%s' is considered valid", name)
		}
	}

	// Explicity test: "adderall"
	if Valid("adderall") {
		t.Error("Denylisted drug spam name 'viagra' is considered valid")
	}
	if Valid("adderallbox") {
		t.Error("Name containing denylisted drug spam 'viagra' is considered valid")
	}
	if Valid("allOfTheAdderallRightNow") {
		t.Error("Name containing denylisted drug spam 'viagra' is considered valid")
	}
}
