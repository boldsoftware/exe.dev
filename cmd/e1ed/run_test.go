package main

import (
	"testing"
)

func TestRunRequestValidateDefaults(t *testing.T) {
	req := RunRequest{Commit: "abc123"}
	if err := req.validate(); err != nil {
		t.Fatal(err)
	}
	if len(req.Flags) == 0 {
		t.Fatal("expected default flags")
	}
	if req.Flags[0] != "-race" {
		t.Fatalf("expected -race as first flag, got %q", req.Flags[0])
	}
	if len(req.Packages) == 0 {
		t.Fatal("expected default packages")
	}
	if req.Packages[0] != "./e1e" {
		t.Fatalf("expected ./e1e as first package, got %q", req.Packages[0])
	}
}

func TestRunRequestValidateMissingCommit(t *testing.T) {
	req := RunRequest{}
	if err := req.validate(); err == nil {
		t.Fatal("expected error for missing commit")
	}
}

func TestRunRequestValidateCustomFlags(t *testing.T) {
	req := RunRequest{
		Commit:   "abc123",
		Flags:    []string{"-run=TestFoo"},
		Packages: []string{"./e1e"},
	}
	if err := req.validate(); err != nil {
		t.Fatal(err)
	}
	if len(req.Flags) != 1 || req.Flags[0] != "-run=TestFoo" {
		t.Fatalf("flags should be preserved: %v", req.Flags)
	}
	if len(req.Packages) != 1 || req.Packages[0] != "./e1e" {
		t.Fatalf("packages should be preserved: %v", req.Packages)
	}
}

func TestTrimNewline(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"hello\n", "hello"},
		{"hello\r\n", "hello"},
		{"hello", "hello"},
		{"\n", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := trimNewline(tt.in)
		if got != tt.want {
			t.Errorf("trimNewline(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMinInt(t *testing.T) {
	if minInt(3, 5) != 3 {
		t.Fatal("expected 3")
	}
	if minInt(5, 3) != 3 {
		t.Fatal("expected 3")
	}
	if minInt(3, 3) != 3 {
		t.Fatal("expected 3")
	}
}
