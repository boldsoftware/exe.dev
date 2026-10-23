package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boldsoftware/exe.dev/exe-tty/exescroll"
	"github.com/coder/websocket"
)

// The server runs its own executable as `exe-scroll`; in tests that is the
// test binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "exe-scroll" {
		err := exescroll.Exec(os.Args[2:])
		println("exe-scroll:", err.Error())
		os.Exit(1)
	}
	os.Exit(m.Run())
}

type harness struct {
	t      *testing.T
	srv    *server
	ts     *httptest.Server
	client *http.Client
}

func newHarness(t *testing.T) *harness {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	srv, err := newServer("secret")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	h := &harness{t: t, srv: srv, ts: ts, client: &http.Client{Jar: jar}}
	resp, err := h.client.Get(ts.URL + "/login?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(jar.Cookies(resp.Request.URL)) != 1 {
		t.Fatalf("login set no cookie")
	}
	// Sessions outlive the server; end the ones the test started.
	t.Cleanup(func() {
		for _, name := range h.sessions() {
			resp, err := h.client.PostForm(ts.URL+"/terminal/sessions/close", url.Values{"name": {name}})
			if err == nil {
				resp.Body.Close()
			}
		}
	})
	return h
}

func (h *harness) dial(name string) *websocket.Conn {
	h.t.Helper()
	u, _ := url.Parse(h.ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.ts.URL, "http")+"/terminal/ws/1?name="+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {h.srv.cookie + "=" + h.client.Jar.Cookies(u)[0].Value}},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, m message) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// expect reads output until it contains want. Output includes the echo of
// input, so want should be something only the command's output contains.
func expect(t *testing.T, c *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var seen strings.Builder
	for !strings.Contains(seen.String(), want) {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; got %q", want, err, seen.String())
		}
		var m message
		json.Unmarshal(b, &m)
		if m.Type == "output" {
			out, _ := base64.StdEncoding.DecodeString(m.Data)
			seen.Write(out)
		}
	}
}

// expectClose reads until the server closes the socket and returns the code.
func expectClose(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func (h *harness) sessions() []string {
	h.t.Helper()
	resp, err := h.client.Get(h.ts.URL + "/terminal/sessions")
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Sessions []struct{ Name string } }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, s := range body.Sessions {
		names = append(names, s.Name)
	}
	return names
}

func requireEmbedded(t *testing.T) {
	if !exescroll.Embedded() {
		t.Skip("exe-scroll not embedded; run make test")
	}
}

func TestAuth(t *testing.T) {
	h := newHarness(t)
	for _, u := range []string{"/", "/terminal/sessions", "/login?token=wrong"} {
		resp, err := http.Get(h.ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without cookie: %d", u, resp.StatusCode)
		}
	}
	resp, err := h.client.Get(h.ts.URL + "/assets/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /assets/: %d, want no listing", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", h.ts.URL+"/terminal/sessions/close?name=x", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST: %d", resp.StatusCode)
	}
}

// Cookies ignore ports, so two servers on one host (say, two `ssh -L`
// forwards) must not log each other out.
func TestTwoServers(t *testing.T) {
	h := newHarness(t)
	other, err := newServer("other")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(other)
	defer ts.Close()
	resp, err := h.client.Get(ts.URL + "/login?token=other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, u := range []string{h.ts.URL + "/terminal/sessions", ts.URL + "/terminal/sessions"} {
		resp, err := h.client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d", u, resp.StatusCode)
		}
	}
}

// The shell gets a terminal of its own, not the one exe-tty was started in.
func TestShellEnvironment(t *testing.T) {
	requireEmbedded(t)
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")
	t.Setenv("TERM_PROGRAM", "outer")
	h := newHarness(t)
	c := h.dial("env")
	send(t, c, message{Type: "input", Data: "echo env-$((1+1))-[$TMUX$TERM_PROGRAM]-$TERM\r"})
	expect(t, c, "env-2-[]-xterm-256color")
}

func TestSessionLifecycle(t *testing.T) {
	requireEmbedded(t)
	h := newHarness(t)

	c := h.dial("amber")
	send(t, c, message{Type: "input", Data: "echo hello-$((6*7))\r"})
	expect(t, c, "hello-42")
	if got := h.sessions(); len(got) != 1 || got[0] != "amber" {
		t.Fatalf("sessions = %q", got)
	}

	// Detaching leaves the shell running; reattaching finds its variable.
	send(t, c, message{Type: "input", Data: "X=kept; echo set-$X\r"})
	expect(t, c, "set-kept")
	c.Close(websocket.StatusNormalClosure, "")
	c = h.dial("amber")
	send(t, c, message{Type: "resize", Cols: 100, Rows: 30})
	send(t, c, message{Type: "input", Data: "echo $X-$(stty size)\r"})
	expect(t, c, "kept-30 100")

	// The shell exiting ends the session.
	send(t, c, message{Type: "input", Data: "exit\r"})
	if code := expectClose(t, c); code != closeShellExited {
		t.Fatalf("close code = %d, want %d", code, closeShellExited)
	}

	// Closing a session ends it for everyone attached.
	c = h.dial("coral")
	send(t, c, message{Type: "input", Data: "echo ready-$((1+1))\r"})
	expect(t, c, "ready-2")
	resp, err := h.client.PostForm(h.ts.URL+"/terminal/sessions/close", url.Values{"name": {"coral"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("close: %d", resp.StatusCode)
	}
	if code := expectClose(t, c); code != closeShellExited {
		t.Fatalf("close code = %d, want %d", code, closeShellExited)
	}
	if got := h.sessions(); len(got) != 0 {
		t.Fatalf("sessions after close = %q", got)
	}
}

// paste sends n bytes of input the way the web app does, in 4 KiB pieces,
// giving up after timeout.
func paste(c *websocket.Conn, n int, timeout time.Duration) {
	chunk, _ := json.Marshal(message{Type: "input", Data: strings.Repeat("x", 4<<10)})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for range n / (4 << 10) {
		if c.Write(ctx, websocket.MessageText, chunk) != nil {
			return
		}
	}
}

// A paste bigger than every buffer on the way arrives whole.
func TestBigPaste(t *testing.T) {
	requireEmbedded(t)
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "paste")
	c := h.dial("paste")
	send(t, c, message{Type: "input", Data: "stty -echo -icanon; echo pasting-$((1+1)); exec cat > " + out + "\r"})
	expect(t, c, "pasting-2")
	const n = 8 << 20
	paste(c, n, 30*time.Second)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		fi, err := os.Stat(out)
		if err == nil && fi.Size() == n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pasted %d bytes, got %v %v", n, fi, err)
		}
	}
}

// A browser that leaves must be detached even when the shell has stopped
// reading what it typed.
func TestStalledShellDetaches(t *testing.T) {
	requireEmbedded(t)
	h := newHarness(t)
	h.srv.stall = time.Second
	c := h.dial("stall")
	send(t, c, message{Type: "input", Data: "stty -echo -icanon; echo stalled-$((1+1)); sleep 600\r"})
	expect(t, c, "stalled-2")
	// More than exe-scroll, the PTYs, and exe-tty will buffer.
	attach := "exe-scroll: attach " + h.srv.socket("stall")
	if pidOf(attach) == 0 {
		t.Fatal("no attach client")
	}
	paste(c, 8<<20, 2*time.Second)
	c.CloseNow()

	for deadline := time.Now().Add(10 * time.Second); pidOf(attach) != 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("attach client still running after the browser left")
		}
	}
}

func TestPing(t *testing.T) {
	requireEmbedded(t)
	h := newHarness(t)
	c := h.dial("ping")
	send(t, c, message{Type: "ping"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m message
		json.Unmarshal(b, &m)
		if m.Type == "pong" {
			return
		}
	}
}

func TestUpload(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	upload := func(name string) (int, map[string]string) {
		resp, err := h.client.Post(h.ts.URL+"/terminal/upload?"+url.Values{"filename": {name}, "dir": {dir}}.Encode(), "text/plain", strings.NewReader("hi"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	code, body := upload("../my notes.txt")
	if want := filepath.Join(dir, "my_notes.txt"); code != 200 || body["path"] != want {
		t.Fatalf("upload = %d %v, want %s", code, body, want)
	}
	_, again := upload("my notes.txt")
	if again["path"] == body["path"] || filepath.Ext(again["path"]) != ".txt" {
		t.Fatalf("second upload overwrote or lost the extension: %v", again)
	}
	if b, err := os.ReadFile(again["path"]); err != nil || string(b) != "hi" {
		t.Fatalf("read back %q, %v", b, err)
	}
}

func TestCleanFilename(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt":        "a.txt",
		"../../etc/x":  "x",
		`C:\dir\y.png`: "y.png",
		".hidden":      "hidden",
		"we ird*name!": "we_ird_name",
	} {
		if got := cleanFilename(in); got != want {
			t.Errorf("cleanFilename(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanFilename("..."); !strings.HasPrefix(got, "upload_") {
		t.Errorf("cleanFilename(...) = %q", got)
	}
}
