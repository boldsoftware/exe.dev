package main

import (
	"strings"
	"testing"
)

func TestCheckClickhouseStructural_OK(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"SELECT count() FROM default.events WHERE user_id = 'abc''def'",
		"  WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW TABLES",
		"DESCRIBE default.events",
		"DESC default.events",
		"EXPLAIN SELECT 1",
		"EXISTS TABLE default.events",
		"SELECT 1;", // trailing semicolon fine
		"SELECT /* harmless comment with url('x') */ 1",
		"SELECT 'url(' || name FROM default.events LIMIT 1",
		"SELECT 1 -- url('x')\n",
		"SELECT `file` FROM default.events", // identifier named file is fine
	}
	for _, q := range ok {
		if err := checkClickhouseStructural(q); err != nil {
			t.Errorf("expected OK, got error for %q: %v", q, err)
		}
	}
}

func TestCheckClickhouseStructural_Rejected(t *testing.T) {
	cases := []struct {
		q    string
		want string // substring expected in error
	}{
		{"", "empty"},
		{"INSERT INTO t VALUES (1)", "must start"},
		{"DROP TABLE t", "must start"},
		{"SELECT 1; DROP TABLE t", "multiple statements"},
		{"SELECT * FROM url('https://example.com', 'CSV', 'a String')", "url"},
		{"SELECT * FROM URL('https://x', 'CSV', 'a String')", "URL"},
		{"SELECT * FROM s3('x','y','z')", "s3"},
		{"SELECT * FROM file('/etc/passwd','LineAsString')", "file"},
		{"SELECT * FROM remote('127.0.0.1', system.one)", "remote"},
		{"SELECT * FROM mysql('h','d','t','u','p')", "mysql"},
		{"SELECT * FROM executable('echo hi','TSV','a String')", "executable"},
		{"SELECT dictGet('d','a',toUInt64(1))", "dictGet"},
		{"SELECT 1 INTO OUTFILE '/tmp/x'", "OUTFILE"},
		{"SELECT 1 SETTINGS readonly=0", "SETTINGS"},
		{"SYSTEM DROP DNS CACHE", "must start"},
	}
	for _, c := range cases {
		err := checkClickhouseStructural(c.q)
		if err == nil {
			t.Errorf("expected error for %q, got none", c.q)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("error for %q = %v; want substring %q", c.q, err, c.want)
		}
	}
}

func TestStripSQLNoise(t *testing.T) {
	in := "SELECT 'url(''x'')', `file`, \"s3\" -- url('a')\nFROM /* file() */ t"
	out := stripSQLNoise(in)
	// Dangerous tokens inside strings/comments must be gone.
	for _, bad := range []string{"url(", "file(", "s3"} {
		if strings.Contains(strings.ToLower(out), bad) {
			t.Errorf("stripSQLNoise left %q in: %q", bad, out)
		}
	}
	// Structural keywords preserved.
	for _, good := range []string{"SELECT", "FROM", "t"} {
		if !strings.Contains(out, good) {
			t.Errorf("stripSQLNoise dropped %q from: %q", good, out)
		}
	}
	if len(out) != len(in) {
		t.Errorf("length changed: %d -> %d", len(in), len(out))
	}
}
