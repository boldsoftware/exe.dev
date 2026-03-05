package testinfra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func jsonLine(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func writeLines(w io.Writer, lines ...string) {
	for _, line := range lines {
		fmt.Fprintf(w, "%s\n", line)
	}
}

func fullStartupLines(sshPort, httpPort, pluginPort, exeproxPort int, extraPorts []int) []string {
	lines := []string{
		jsonLine(map[string]any{"msg": "listening", "type": "ssh", "port": float64(sshPort)}),
		jsonLine(map[string]any{"msg": "listening", "type": "http", "port": float64(httpPort)}),
		jsonLine(map[string]any{"msg": "listening", "type": "plugin", "port": float64(pluginPort)}),
		jsonLine(map[string]any{"msg": "listening", "type": "exeprox-service", "port": float64(exeproxPort)}),
	}
	if extraPorts != nil {
		portsAny := make([]any, len(extraPorts))
		for i, p := range extraPorts {
			portsAny[i] = float64(p)
		}
		lines = append(lines, jsonLine(map[string]any{"msg": "proxy listeners set up", "ports": portsAny}))
	}
	lines = append(lines, jsonLine(map[string]any{"msg": "server started"}))
	return lines
}

func TestWatchExedLogsHappyPath(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	extraPorts := []int{8080, 9090}
	lines := fullStartupLines(2222, 3333, 4444, 5555, extraPorts)

	go func() {
		defer pw.Close()
		writeLines(pw, lines...)
	}()

	result, err := watchExedLogs(context.Background(), pr, nil, false, 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if result.Ports.SSH != 2222 {
		t.Errorf("SSH port = %d, want 2222", result.Ports.SSH)
	}
	if result.Ports.HTTP != 3333 {
		t.Errorf("HTTP port = %d, want 3333", result.Ports.HTTP)
	}
	if result.Ports.PiperPlugin != 4444 {
		t.Errorf("PiperPlugin port = %d, want 4444", result.Ports.PiperPlugin)
	}
	if result.Ports.Exeprox != 5555 {
		t.Errorf("Exeprox port = %d, want 5555", result.Ports.Exeprox)
	}
	if len(result.Ports.Extra) != 2 || result.Ports.Extra[0] != 8080 || result.Ports.Extra[1] != 9090 {
		t.Errorf("Extra ports = %v, want [8080 9090]", result.Ports.Extra)
	}
}

func TestWatchExedLogsMissingPort(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	go func() {
		defer pw.Close()
		writeLines(pw,
			jsonLine(map[string]any{"msg": "listening", "type": "ssh", "port": float64(2222)}),
			jsonLine(map[string]any{"msg": "listening", "type": "http", "port": float64(3333)}),
			jsonLine(map[string]any{"msg": "listening", "type": "plugin", "port": float64(4444)}),
			// Missing exeprox-service
			jsonLine(map[string]any{"msg": "server started"}),
		)
	}()

	_, err := watchExedLogs(context.Background(), pr, nil, false, 0, 5*time.Second)
	if err == nil {
		t.Fatal("expected error for missing port")
	}
	if !strings.Contains(err.Error(), "exeprox 0") {
		t.Errorf("error = %q, want mention of exeprox 0", err.Error())
	}
}

func TestWatchExedLogsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pr.Close()
		defer pw.Close()

		// Write something but never "server started".
		go writeLines(pw, jsonLine(map[string]any{"msg": "listening", "type": "ssh", "port": float64(2222)}))

		_, err := watchExedLogs(context.Background(), pr, nil, false, 0, 100*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Errorf("error = %q, want timeout message", err.Error())
		}
	})
}

func TestWatchExedLogsErrorForwarding(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	go func() {
		defer pw.Close()
		writeLines(pw,
			jsonLine(map[string]any{"level": "ERROR", "msg": "something went wrong"}),
		)
		writeLines(pw, fullStartupLines(2222, 3333, 4444, 5555, nil)...)
	}()

	result, err := watchExedLogs(context.Background(), pr, nil, false, 0, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case errMsg := <-result.Errors:
		if !strings.Contains(errMsg, "something went wrong") {
			t.Errorf("error = %q, want 'something went wrong'", errMsg)
		}
	case <-time.After(time.Second):
		t.Error("expected error on Errors channel")
	}
}

func TestWatchExedLogsGUIDForwarding(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	guid := "12345678-1234-1234-1234-123456789abc"
	go func() {
		defer pw.Close()
		writeLines(pw,
			jsonLine(map[string]any{"guid": guid, "msg": "some operation"}),
		)
		writeLines(pw, fullStartupLines(2222, 3333, 4444, 5555, nil)...)
	}()

	result, err := watchExedLogs(context.Background(), pr, nil, false, 0, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case guidMsg := <-result.GUIDLog:
		if !strings.Contains(guidMsg, guid) {
			t.Errorf("GUID log = %q, want to contain %q", guidMsg, guid)
		}
	case <-time.After(time.Second):
		t.Error("expected message on GUIDLog channel")
	}
}

func TestWatchExedLogsLogFile(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	var logBuf bytes.Buffer
	lines := fullStartupLines(2222, 3333, 4444, 5555, nil)

	go func() {
		defer pw.Close()
		writeLines(pw, lines...)
	}()

	_, err := watchExedLogs(context.Background(), pr, &logBuf, false, 0, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	logged := logBuf.String()
	for _, line := range lines {
		if !strings.Contains(logged, line) {
			t.Errorf("logFile missing line: %s", line)
		}
	}
}
