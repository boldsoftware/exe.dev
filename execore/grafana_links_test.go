package execore

import (
	"net/url"
	"strings"
	"testing"
)

func TestVMGrafanaLinks(t *testing.T) {
	links := vmGrafanaLinks("orange-popcorn")
	if len(links) == 0 {
		t.Fatal("expected some links")
	}
	seen := map[string]bool{}
	for _, l := range links {
		if l.Label == "" || l.URL == "" {
			t.Errorf("empty link: %+v", l)
		}
		seen[l.Label] = true
		u, err := url.Parse(l.URL)
		if err != nil {
			t.Fatalf("bad URL %q: %v", l.URL, err)
		}
		if u.Host != "grafana.crocodile-vector.ts.net" {
			t.Errorf("unexpected host: %s", u.Host)
		}
		panes := u.Query().Get("panes")
		if !strings.Contains(panes, `vm_name=\"orange-popcorn\"`) {
			t.Errorf("panes missing vm_name selector: %s", panes)
		}
	}
	for _, want := range []string{
		"Network rx + tx bytes/sec",
		"Disk I/O read + write bytes/sec",
		"Memory + swap bytes",
	} {
		if !seen[want] {
			t.Errorf("missing paired link %q", want)
		}
	}
}
