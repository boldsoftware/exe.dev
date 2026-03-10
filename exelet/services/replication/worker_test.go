package replication

import (
	"log/slog"
	"runtime"
	"testing"
)

func TestNewWorkerPool_WorkerCount(t *testing.T) {
	log := slog.Default()
	state, err := NewState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metrics := NewMetrics(nil)
	noopRestoring := func(string) bool { return false }

	tests := []struct {
		name    string
		workers int
		want    int
	}{
		{
			name:    "auto defaults to NumCPU/4 min 1",
			workers: 0,
			want:    max(runtime.NumCPU()/4, 1),
		},
		{
			name:    "explicit worker count honored",
			workers: 8,
			want:    8,
		},
		{
			name:    "single worker",
			workers: 1,
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wp := NewWorkerPool(nil, state, metrics, 1, tt.workers, log, noopRestoring)
			defer wp.Stop()

			if wp.workerCount != tt.want {
				t.Errorf("workerCount = %d, want %d", wp.workerCount, tt.want)
			}
		})
	}
}
