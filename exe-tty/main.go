// Command exe-tty serves a terminal on this machine to a web browser.
//
// Shells run inside exe-scroll sessions (embedded in this binary), so they
// survive page reloads, dropped connections, and restarts of exe-tty itself.
// See README.md for usage and API.md for the browser/server protocol.
package main

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/boldsoftware/exe.dev/exe-tty/exescroll"
)

var version = "dev"

func main() {
	// The server runs its own binary as `exe-tty exe-scroll ...` for each
	// attachment; see exescroll.
	if len(os.Args) > 1 && os.Args[1] == "exe-scroll" {
		args := os.Args[2:]
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			// Start a new session as the server would, so it can find it.
			args = append(args, "--", loginShell(), "-l")
		}
		err := exescroll.Exec(args)
		fmt.Fprintln(os.Stderr, "exe-tty:", err)
		os.Exit(1)
	}
	addr := flag.String("addr", "localhost:7600", "listen address")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("exe-tty %s, with %s\n", version, cmp.Or(exescroll.Release(), "no exe-scroll"))
		return
	}
	if err := run(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "exe-tty:", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	if !exescroll.Embedded() {
		return errors.New("exe-scroll is not embedded in this build; build with make")
	}
	token, err := loadToken()
	if err != nil {
		return err
	}
	srv, err := newServer(token)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	tcp := ln.Addr().(*net.TCPAddr)
	host := tcp.IP.String()
	if tcp.IP.IsLoopback() || tcp.IP.IsUnspecified() {
		host = "localhost"
	}
	fmt.Printf("exe-tty %s: http://%s/login?token=%s\n", version, net.JoinHostPort(host, strconv.Itoa(tcp.Port)), token)
	return http.Serve(ln, srv)
}

// loadToken returns the login secret, creating it on first run. Anyone with
// it can run commands as this user, so it is kept private to the user.
func loadToken() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "exe-tty", "token")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}
