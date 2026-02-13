package resourcemanager

import "testing"

func TestParseIOStat(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantRead  uint64
		wantWrite uint64
	}{
		{
			name:      "single device",
			input:     "254:0 rbytes=1000 wbytes=2000 rios=10 wios=20 dbytes=0 dios=0\n",
			wantRead:  1000,
			wantWrite: 2000,
		},
		{
			name:      "multiple devices summed",
			input:     "254:0 rbytes=1000 wbytes=2000 rios=10 wios=20 dbytes=0 dios=0\n230:16 rbytes=500 wbytes=300 rios=5 wios=3 dbytes=0 dios=0\n",
			wantRead:  1500,
			wantWrite: 2300,
		},
		{
			name:      "empty lines ignored",
			input:     "7:0 \n254:0 rbytes=100 wbytes=200 rios=1 wios=2 dbytes=0 dios=0\n",
			wantRead:  100,
			wantWrite: 200,
		},
		{
			name:      "empty file",
			input:     "",
			wantRead:  0,
			wantWrite: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRead, gotWrite, err := parseIOStat([]byte(tt.input))
			if err != nil {
				t.Fatalf("parseIOStat() error = %v", err)
			}
			if gotRead != tt.wantRead {
				t.Errorf("readBytes = %d, want %d", gotRead, tt.wantRead)
			}
			if gotWrite != tt.wantWrite {
				t.Errorf("writeBytes = %d, want %d", gotWrite, tt.wantWrite)
			}
		})
	}
}
