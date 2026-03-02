package templates

import (
	"io/fs"
	"regexp"
	"testing"
)

var scriptTagRe = regexp.MustCompile(`<script\s[^>]*src="https://cdn\.jsdelivr\.net/[^"]*"[^>]*>`)

func TestCDNScriptsHaveSRI(t *testing.T) {
	err := fs.WalkDir(Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(Files, path)
		if err != nil {
			return err
		}
		content := string(data)
		matches := scriptTagRe.FindAllString(content, -1)
		for _, tag := range matches {
			t.Run(path, func(t *testing.T) {
				if matched, _ := regexp.MatchString(`integrity="sha(256|384|512)-[A-Za-z0-9+/=]+"`, tag); !matched {
					t.Errorf("missing integrity attribute:\n  %s", tag)
				}
				if matched, _ := regexp.MatchString(`crossorigin="anonymous"`, tag); !matched {
					t.Errorf("missing crossorigin attribute:\n  %s", tag)
				}
				if matched, _ := regexp.MatchString(`@\d+\.\d+\.\d+`, tag); !matched {
					t.Errorf("version not pinned (use exact semver, not range):\n  %s", tag)
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded files: %v", err)
	}
}
