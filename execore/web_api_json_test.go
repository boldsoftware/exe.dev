package execore

import "testing"

func TestFormatPoolSize(t *testing.T) {
	tests := []struct {
		cpus     uint64
		memoryGB uint64
		want     string
	}{
		{2, 8, "2 vCPUs \u00b7 8 GB memory"},
		{4, 16, "4 vCPUs \u00b7 16 GB memory"},
		{16, 64, "16 vCPUs \u00b7 64 GB memory"},
		{0, 0, ""},
	}
	for _, tt := range tests {
		got := formatPoolSize(tt.cpus, tt.memoryGB)
		if got != tt.want {
			t.Errorf("formatPoolSize(%d, %d) = %q, want %q", tt.cpus, tt.memoryGB, got, tt.want)
		}
	}
}

func TestMonthlyResetAtText(t *testing.T) {
	// When plan has monthly refresh, should return a non-empty formatted timestamp.
	got := monthlyResetAtText(true)
	if got == "" {
		t.Error("monthlyResetAtText(true) returned empty string, want non-empty timestamp")
	}

	// When plan does NOT have monthly refresh, should return empty string.
	got = monthlyResetAtText(false)
	if got != "" {
		t.Errorf("monthlyResetAtText(false) = %q, want empty string", got)
	}
}

func TestAllKnownTagsForIncludesIntegrationOnlyTags(t *testing.T) {
	tagVMs := map[string][]string{
		"web":     {"vm-a"},
		"vm-only": {"vm-b"},
	}
	integrations := []jsonIntegrationInfo{
		{Name: "openai", Attachments: []string{"tag:web", "tag:llm", "box:vm-a"}},
		{Name: "anthropic", Attachments: []string{"tag:llm"}}, // dup with openai
		{Name: "github", Attachments: []string{"tag:", ""}},
	}
	got := allKnownTagsFor(tagVMs, integrations)
	want := []string{"llm", "vm-only", "web"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}
