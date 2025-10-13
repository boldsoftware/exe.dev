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

func TestDrugSpamDenylist(t *testing.T) {
	t.Parallel()
	for _, name := range drugspam {
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
