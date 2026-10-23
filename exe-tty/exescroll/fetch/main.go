// Command fetch downloads the newest exe-scroll release binary, checked
// against the SHA-256 digest GitHub publishes for it:
//
//	go run ./exescroll/fetch exescroll/binary/exe-scroll-linux-amd64
//
// It also writes the release's tag next to it, with a .tag suffix. Releases
// are only considered once they have binaries for every platform, so all
// platforms get the same one.
package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const releases = "https://api.github.com/repos/boldsoftware/exe.dev/releases?per_page=100"

var platforms = []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"}

type asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	URL    string `json:"browser_download_url"`
}

type release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fetch <dir>/exe-scroll-<goos>-<goarch>")
		os.Exit(2)
	}
	if err := fetch(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
}

func fetch(path string) error {
	tag, a, err := latest(filepath.Base(path))
	if err != nil {
		return err
	}
	b, err := get(a.URL)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(b); "sha256:"+hex.EncodeToString(sum[:]) != a.Digest {
		return fmt.Errorf("%s %s: digest mismatch", tag, a.Name)
	}
	if err := os.WriteFile(path+".tmp", b, 0o755); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	fmt.Printf("%s <- %s\n", path, tag)
	return os.WriteFile(path+".tag", []byte(tag+"\n"), 0o644)
}

// latest finds the newest complete exe-scroll release, and its asset name.
func latest(name string) (string, asset, error) {
	b, err := get(releases)
	if err != nil {
		return "", asset{}, err
	}
	var rs []release
	if err := json.Unmarshal(b, &rs); err != nil {
		return "", asset{}, err
	}
	for _, r := range rs {
		if !strings.HasPrefix(r.Tag, "exe-scroll/") || r.Draft || r.Prerelease {
			continue
		}
		var have []string
		var found asset
		for _, a := range r.Assets {
			if strings.HasPrefix(a.Digest, "sha256:") {
				have = append(have, strings.TrimPrefix(a.Name, "exe-scroll-"))
			}
			if a.Name == name {
				found = a
			}
		}
		if found.Name != "" && !slices.ContainsFunc(platforms, func(p string) bool { return !slices.Contains(have, p) }) {
			return r.Tag, found, nil
		}
	}
	return "", asset{}, fmt.Errorf("no exe-scroll release has %s", name)
}

func get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// CI runners share IP addresses, and so GitHub's anonymous rate limit.
	if t := cmp.Or(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")); t != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
