package resourcemanager

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	"exe.dev/exelet/storage/ext4"
	api "exe.dev/pkg/api/exe/resource/v1"
)

// fakeStream captures ListVMUsage responses in memory.
type fakeStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*api.VMUsage
}

func (s *fakeStream) Send(r *api.ListVMUsageResponse) error {
	s.sent = append(s.sent, r.GetUsage())
	return nil
}
func (s *fakeStream) Context() context.Context { return s.ctx }

// usageWithFs is a canned ext4.Usage for the test hook.
var usageWithFs = ext4.Usage{BlockSize: 4096, TotalBlocks: 1024, FreeBlocks: 512, ReservedBlocks: 0}

func newRMForGateTest(t *testing.T, allowGroup string, envEnabled bool) *ResourceManager {
	t.Helper()
	allow := map[string]struct{}{}
	if allowGroup != "" {
		allow[allowGroup] = struct{}{}
	}
	m := &ResourceManager{
		collectExt4Usage:         envEnabled,
		collectExt4UsageGroupIDs: allow,
		usageState: map[string]*vmUsageState{
			"vm-allowed": {name: "allowed-box", groupID: "usrALLOW"},
			"vm-other":   {name: "other-box", groupID: "usrOTHER"},
		},
		readFilesystemUsageFn: func(_ context.Context, _ string) (ext4.Usage, bool) {
			return usageWithFs, true
		},
	}
	return m
}

func TestGetVMUsageGate(t *testing.T) {
	t.Parallel()
	t.Run("flag false: no fs fields", func(t *testing.T) {
		m := newRMForGateTest(t, "usrALLOW", true)
		resp, err := m.GetVMUsage(t.Context(), &api.GetVMUsageRequest{VmID: "vm-allowed"})
		if err != nil {
			t.Fatal(err)
		}
		if u := resp.GetUsage(); u.FsTotalBytes != 0 || u.FsFreeBytes != 0 || u.FsAvailableBytes != 0 {
			t.Fatalf("fs fields leaked when flag=false: %+v", u)
		}
	})
	t.Run("flag true, gate denies: no fs fields", func(t *testing.T) {
		m := newRMForGateTest(t, "usrALLOW", false /*env-wide off*/)
		resp, err := m.GetVMUsage(t.Context(), &api.GetVMUsageRequest{VmID: "vm-other", CollectFilesystemUsage: true})
		if err != nil {
			t.Fatal(err)
		}
		if u := resp.GetUsage(); u.FsTotalBytes != 0 {
			t.Fatalf("gate failed: fs returned for non-allow-listed group: %+v", u)
		}
	})
	t.Run("flag true, gate allows by group", func(t *testing.T) {
		m := newRMForGateTest(t, "usrALLOW", false)
		resp, err := m.GetVMUsage(t.Context(), &api.GetVMUsageRequest{VmID: "vm-allowed", CollectFilesystemUsage: true})
		if err != nil {
			t.Fatal(err)
		}
		u := resp.GetUsage()
		if u.FsTotalBytes != usageWithFs.TotalBytes() {
			t.Fatalf("FsTotalBytes = %d, want %d", u.FsTotalBytes, usageWithFs.TotalBytes())
		}
		if u.FsFreeBytes == 0 {
			t.Fatal("FsFreeBytes = 0")
		}
	})
	t.Run("flag true, gate allows by env", func(t *testing.T) {
		m := newRMForGateTest(t, "", true /*env-wide on*/)
		resp, err := m.GetVMUsage(t.Context(), &api.GetVMUsageRequest{VmID: "vm-other", CollectFilesystemUsage: true})
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetUsage().FsTotalBytes == 0 {
			t.Fatal("env-wide gate did not allow")
		}
	})
}

func TestListVMUsageGate(t *testing.T) {
	t.Parallel()
	t.Run("flag false: zero on all rows", func(t *testing.T) {
		m := newRMForGateTest(t, "usrALLOW", true)
		s := &fakeStream{ctx: t.Context()}
		if err := m.ListVMUsage(&api.ListVMUsageRequest{}, s); err != nil {
			t.Fatal(err)
		}
		if len(s.sent) != 2 {
			t.Fatalf("got %d rows", len(s.sent))
		}
		for _, u := range s.sent {
			if u.FsTotalBytes != 0 {
				t.Errorf("%s: fs leaked: %+v", u.Name, u)
			}
		}
	})
	t.Run("flag true, env on: populated for both", func(t *testing.T) {
		m := newRMForGateTest(t, "", true)
		s := &fakeStream{ctx: t.Context()}
		if err := m.ListVMUsage(&api.ListVMUsageRequest{CollectFilesystemUsage: true}, s); err != nil {
			t.Fatal(err)
		}
		for _, u := range s.sent {
			if u.FsTotalBytes == 0 {
				t.Errorf("%s: FsTotalBytes=0 with env-wide gate on", u.Name)
			}
		}
	})
	t.Run("flag true, allow-list only: only matching group populated", func(t *testing.T) {
		m := newRMForGateTest(t, "usrALLOW", false)
		s := &fakeStream{ctx: t.Context()}
		if err := m.ListVMUsage(&api.ListVMUsageRequest{CollectFilesystemUsage: true}, s); err != nil {
			t.Fatal(err)
		}
		var allowedFs, otherFs uint64
		for _, u := range s.sent {
			switch u.Name {
			case "allowed-box":
				allowedFs = u.FsTotalBytes
			case "other-box":
				otherFs = u.FsTotalBytes
			}
		}
		if allowedFs == 0 {
			t.Errorf("allowed-box not populated")
		}
		if otherFs != 0 {
			t.Errorf("other-box leaked fs: %d", otherFs)
		}
	})
}
