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

func newRMForFsTest(t *testing.T) *ResourceManager {
	t.Helper()
	m := &ResourceManager{
		usageState: map[string]*vmUsageState{
			"vm-a": {name: "box-a", groupID: "usrA"},
			"vm-b": {name: "box-b", groupID: "usrB"},
		},
		readFilesystemUsageFn: func(_ context.Context, _ string) (ext4.Usage, bool) {
			return usageWithFs, true
		},
	}
	return m
}

// TestGetVMUsageFsFlag: the request flag controls whether ext4 usage is
// probed and returned. There is no host-side gate.
func TestGetVMUsageFsFlag(t *testing.T) {
	t.Parallel()
	t.Run("flag false: no fs fields", func(t *testing.T) {
		m := newRMForFsTest(t)
		resp, err := m.GetVMUsage(t.Context(), &api.GetVMUsageRequest{VmID: "vm-a"})
		if err != nil {
			t.Fatal(err)
		}
		if u := resp.GetUsage(); u.FsTotalBytes != 0 || u.FsFreeBytes != 0 || u.FsAvailableBytes != 0 || u.FsUsedBytes != 0 {
			t.Fatalf("fs fields leaked when flag=false: %+v", u)
		}
	})
	t.Run("flag true: populated for any group", func(t *testing.T) {
		m := newRMForFsTest(t)
		resp, err := m.GetVMUsage(t.Context(), &api.GetVMUsageRequest{VmID: "vm-b", CollectFilesystemUsage: true})
		if err != nil {
			t.Fatal(err)
		}
		u := resp.GetUsage()
		if u.FsTotalBytes != usageWithFs.TotalBytes() {
			t.Fatalf("FsTotalBytes = %d, want %d", u.FsTotalBytes, usageWithFs.TotalBytes())
		}
		if u.FsUsedBytes != usageWithFs.UsedBytes() {
			t.Fatalf("FsUsedBytes = %d, want %d", u.FsUsedBytes, usageWithFs.UsedBytes())
		}
	})
}

func TestListVMUsageFsFlag(t *testing.T) {
	t.Parallel()
	t.Run("flag false: zero on all rows", func(t *testing.T) {
		m := newRMForFsTest(t)
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
	t.Run("flag true: populated for all rows", func(t *testing.T) {
		m := newRMForFsTest(t)
		s := &fakeStream{ctx: t.Context()}
		if err := m.ListVMUsage(&api.ListVMUsageRequest{CollectFilesystemUsage: true}, s); err != nil {
			t.Fatal(err)
		}
		for _, u := range s.sent {
			if u.FsTotalBytes == 0 {
				t.Errorf("%s: FsTotalBytes=0", u.Name)
			}
		}
	})
}
