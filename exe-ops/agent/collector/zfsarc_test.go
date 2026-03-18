package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestZFSArcCollect(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantSize    *int64
		wantHitRate *float64
	}{
		{
			name: "standard arcstats",
			content: `5 1 0x01 86 4760 7249482609
name                            type data
hits                            4    1000
misses                          4    250
size                            4    8589934592
`,
			wantSize:    ptrInt64(8589934592),
			wantHitRate: ptrFloat64(80.0),
		},
		{
			name: "all hits",
			content: `5 1 0x01 86 4760 7249482609
name                            type data
hits                            4    500
misses                          4    0
size                            4    1073741824
`,
			wantSize:    ptrInt64(1073741824),
			wantHitRate: ptrFloat64(100.0),
		},
		{
			name: "zero hits and misses",
			content: `5 1 0x01 86 4760 7249482609
name                            type data
hits                            4    0
misses                          4    0
size                            4    4096
`,
			wantSize:    ptrInt64(4096),
			wantHitRate: nil, // total=0, no rate
		},
		{
			name: "size only",
			content: `5 1 0x01 86 4760 7249482609
name                            type data
size                            4    2048
`,
			wantSize:    ptrInt64(2048),
			wantHitRate: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "arcstats")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			z := &ZFSArc{procPath: path}
			if err := z.Collect(context.Background()); err != nil {
				t.Fatalf("Collect: %v", err)
			}

			if tt.wantSize == nil {
				if z.Size != nil {
					t.Errorf("Size = %d, want nil", *z.Size)
				}
			} else {
				if z.Size == nil {
					t.Fatal("Size = nil, want non-nil")
				}
				if *z.Size != *tt.wantSize {
					t.Errorf("Size = %d, want %d", *z.Size, *tt.wantSize)
				}
			}

			if tt.wantHitRate == nil {
				if z.HitRate != nil {
					t.Errorf("HitRate = %f, want nil", *z.HitRate)
				}
			} else {
				if z.HitRate == nil {
					t.Fatal("HitRate = nil, want non-nil")
				}
				if *z.HitRate != *tt.wantHitRate {
					t.Errorf("HitRate = %f, want %f", *z.HitRate, *tt.wantHitRate)
				}
			}
		})
	}
}

func TestZFSArcCollectMissingFile(t *testing.T) {
	z := &ZFSArc{procPath: "/nonexistent/arcstats"}
	if err := z.Collect(context.Background()); err != nil {
		t.Fatalf("Collect should return nil for missing file, got: %v", err)
	}
	if z.Size != nil {
		t.Errorf("Size = %d, want nil", *z.Size)
	}
	if z.HitRate != nil {
		t.Errorf("HitRate = %f, want nil", *z.HitRate)
	}
}

func ptrInt64(v int64) *int64       { return &v }
func ptrFloat64(v float64) *float64 { return &v }
