package execore

import (
	"reflect"
	"testing"
)

func TestParseReflectionFields(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"empty", "", nil, false},
		{"none", "none", nil, false},
		{"all", "all", []string{"all"}, false},
		{"all-with-others", "email,all,tags", []string{"all"}, false},
		{"explicit", "email,tags", []string{"email", "tags"}, false},
		{"dedup-and-sort", "tags,email,tags", []string{"email", "tags"}, false},
		{"unknown", "bogus", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseReflectionFields(c.in)
			if (err != nil) != c.err {
				t.Fatalf("err=%v wantErr=%v", err, c.err)
			}
			if !c.err && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestReflectionFieldEnabled(t *testing.T) {
	if !reflectionFieldEnabled([]string{"all"}, "email") {
		t.Errorf("all should enable email")
	}
	// "all" should also enable any future field name.
	if !reflectionFieldEnabled([]string{"all"}, "future-field-not-yet-defined") {
		t.Errorf("all should enable an unknown future field")
	}
	if reflectionFieldEnabled([]string{"email"}, "tags") {
		t.Errorf("explicit list should not enable absent field")
	}
	if !reflectionFieldEnabled([]string{"email"}, "email") {
		t.Errorf("explicit list should enable listed field")
	}
	if reflectionFieldEnabled(nil, "email") {
		t.Errorf("nil fields should disable everything")
	}
}
