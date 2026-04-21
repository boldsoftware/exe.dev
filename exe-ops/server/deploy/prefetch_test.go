package deploy

import (
	"errors"
	"testing"
)

func TestPrefetcher_StateForUniquePerHost(t *testing.T) {
	r := &rollout{
		waves: []*waveState{
			{requests: []Request{
				{DNSName: "a.test"},
				{DNSName: "b.test"},
			}},
			{requests: []Request{
				{DNSName: "a.test"}, // duplicate dns: shares one entry
				{DNSName: "c.test"},
			}},
		},
	}
	p := newPrefetcher(nil, r)
	if got := len(p.states); got != 3 {
		t.Fatalf("len(states) = %d, want 3", got)
	}
	for _, host := range []string{"a.test", "b.test", "c.test"} {
		if p.stateFor(host) == nil {
			t.Errorf("stateFor(%q) = nil, want non-nil", host)
		}
	}
	if p.stateFor("missing.test") != nil {
		t.Errorf("stateFor(missing) should be nil")
	}
	// Same entry across calls (so a deploy in either wave waits on the
	// same prefetch).
	first := p.stateFor("a.test")
	second := p.stateFor("a.test")
	if first != second {
		t.Errorf("stateFor returned different pointers for the same host")
	}
}

func TestPrefetcher_StateForOnNilReceiver(t *testing.T) {
	var p *prefetcher
	if p.stateFor("anything") != nil {
		t.Errorf("nil prefetcher.stateFor should return nil")
	}
	// wait should be a safe no-op too — finishRollout calls it for
	// rollouts that ran in test mode without a prefetcher.
	p.wait()
}

func TestPrefetcher_FailAllClosesEverything(t *testing.T) {
	r := &rollout{waves: []*waveState{{requests: []Request{
		{DNSName: "a.test"},
		{DNSName: "b.test"},
	}}}}
	p := newPrefetcher(nil, r)

	wantErr := errors.New("boom")
	p.failAll(wantErr)

	for _, host := range []string{"a.test", "b.test"} {
		st := p.stateFor(host)
		select {
		case <-st.done:
		default:
			t.Errorf("%s: done not closed after failAll", host)
		}
		if !errors.Is(st.err, wantErr) {
			t.Errorf("%s: err = %v, want %v", host, st.err, wantErr)
		}
	}

	// Idempotent: calling again must not panic on already-closed done.
	p.failAll(errors.New("again"))
}

func TestTmpUploadPath_DeterministicAndSafe(t *testing.T) {
	r := Recipe{BinaryName: "exeproxd"}
	sha1 := "0123456789abcdef0123456789abcdef01234567"
	sha2 := "fedcba9876543210fedcba9876543210fedcba98"
	if a, b := tmpUploadPath(r, sha1), tmpUploadPath(r, sha1); a != b {
		t.Errorf("not deterministic: %q vs %q", a, b)
	}
	if tmpUploadPath(r, sha1) == tmpUploadPath(r, sha2) {
		t.Errorf("different shas collided: %q", tmpUploadPath(r, sha1))
	}
	if tmpUploadPath(r, sha1) != "/tmp/deploy-exeproxd-0123456789ab" {
		t.Errorf("unexpected path: %q", tmpUploadPath(r, sha1))
	}
}
