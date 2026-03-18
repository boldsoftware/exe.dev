package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostCollect(t *testing.T) {
	dir := t.TempDir()

	uptimePath := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptimePath, []byte("12345.67 98765.43\n"), 0644); err != nil {
		t.Fatal(err)
	}

	loadavgPath := filepath.Join(dir, "loadavg")
	if err := os.WriteFile(loadavgPath, []byte("1.50 2.25 3.75 2/512 12345\n"), 0644); err != nil {
		t.Fatal(err)
	}

	fileNRPath := filepath.Join(dir, "file-nr")
	if err := os.WriteFile(fileNRPath, []byte("1024\t0\t65536\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Host{
		procPath:    uptimePath,
		loadavgPath: loadavgPath,
		fileNRPath:  fileNRPath,
	}

	if err := h.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if h.UptimeSecs != 12345 {
		t.Errorf("UptimeSecs = %d, want 12345", h.UptimeSecs)
	}
	if h.LoadAvg1 != 1.50 {
		t.Errorf("LoadAvg1 = %f, want 1.50", h.LoadAvg1)
	}
	if h.LoadAvg5 != 2.25 {
		t.Errorf("LoadAvg5 = %f, want 2.25", h.LoadAvg5)
	}
	if h.LoadAvg15 != 3.75 {
		t.Errorf("LoadAvg15 = %f, want 3.75", h.LoadAvg15)
	}
	if h.FDAllocated != 1024 {
		t.Errorf("FDAllocated = %d, want 1024", h.FDAllocated)
	}
	if h.FDMax != 65536 {
		t.Errorf("FDMax = %d, want 65536", h.FDMax)
	}
}

func TestHostCollectLoadAvg(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want1   float64
		want5   float64
		want15  float64
		wantErr bool
	}{
		{
			name:    "standard",
			content: "0.01 0.05 0.10 1/200 54321\n",
			want1:   0.01,
			want5:   0.05,
			want15:  0.10,
		},
		{
			name:    "high load",
			content: "48.00 32.00 16.00 5/1024 99999\n",
			want1:   48.00,
			want5:   32.00,
			want15:  16.00,
		},
		{
			name:    "too few fields",
			content: "1.0 2.0\n",
			wantErr: true,
		},
		{
			name:    "non-numeric",
			content: "abc 2.0 3.0 1/1 1\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "loadavg")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			h := &Host{loadavgPath: path}
			err := h.collectLoadAvg()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.LoadAvg1 != tt.want1 {
				t.Errorf("LoadAvg1 = %f, want %f", h.LoadAvg1, tt.want1)
			}
			if h.LoadAvg5 != tt.want5 {
				t.Errorf("LoadAvg5 = %f, want %f", h.LoadAvg5, tt.want5)
			}
			if h.LoadAvg15 != tt.want15 {
				t.Errorf("LoadAvg15 = %f, want %f", h.LoadAvg15, tt.want15)
			}
		})
	}
}

func TestHostCollectFileNR(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantAllocated int64
		wantMax       int64
		wantErr       bool
	}{
		{
			name:          "standard",
			content:       "2048\t0\t131072\n",
			wantAllocated: 2048,
			wantMax:       131072,
		},
		{
			name:          "spaces instead of tabs",
			content:       "512 0 65536\n",
			wantAllocated: 512,
			wantMax:       65536,
		},
		{
			name:    "too few fields",
			content: "1024\t0\n",
			wantErr: true,
		},
		{
			name:    "non-numeric",
			content: "abc\t0\t65536\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file-nr")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			h := &Host{fileNRPath: path}
			err := h.collectFileNR()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.FDAllocated != tt.wantAllocated {
				t.Errorf("FDAllocated = %d, want %d", h.FDAllocated, tt.wantAllocated)
			}
			if h.FDMax != tt.wantMax {
				t.Errorf("FDMax = %d, want %d", h.FDMax, tt.wantMax)
			}
		})
	}
}
