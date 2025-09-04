package container

import (
	"reflect"
	"testing"
)

func TestBuildEntrypointAndCmdArgs_Exeuntu(t *testing.T) {
	tests := []struct {
		name            string
		useExetini      bool
		override        string
		imageEntrypoint []string
		imageCmd        []string
		want            []string
	}{
		{
			name:            "with exetini and image metadata",
			useExetini:      true,
			override:        "",
			imageEntrypoint: nil,
			imageCmd:        []string{"/bin/bash"},
			want:            []string{"-g", "--", "/bin/bash"},
		},
		{
			name:            "with exetini no metadata",
			useExetini:      true,
			override:        "",
			imageEntrypoint: nil,
			imageCmd:        nil,
			want:            []string{"-g", "--", "sleep", "infinity"},
		},
		{
			name:            "with exetini and entrypoint",
			useExetini:      true,
			override:        "",
			imageEntrypoint: []string{"/usr/bin/python"},
			imageCmd:        []string{"-c", "print('hello')"},
			want:            []string{"-g", "--", "/usr/bin/python", "-c", "print('hello')"},
		},
		{
			name:            "without exetini",
			useExetini:      false,
			override:        "",
			imageEntrypoint: nil,
			imageCmd:        []string{"/bin/bash"},
			want:            nil, // Should use image defaults
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildEntrypointAndCmdArgs(tt.useExetini, tt.override, tt.imageEntrypoint, tt.imageCmd)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildEntrypointAndCmdArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
