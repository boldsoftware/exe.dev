//go:build ignore

// Binary exepipe_tester is a test program used by TestExepipeController.
// It is run on a VM. It verifies that exepipe does the
// right thing when it is restarted by systemd.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"exe.dev/exepipe/client"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start reading exepipe output from systemd.
	journal := make(chan string, 1024)
	go func() {
		cmd := exec.CommandContext(ctx, "journalctl", "-u", "exepipe", "-f")
		outputPipe, err := cmd.StdoutPipe()
		if err != nil {
			die("StdoutPipe failed", err)
		}
		var stderrBuf strings.Builder
		cmd.Stderr = &stderrBuf
		if err := cmd.Start(); err != nil {
			die("failed to start journalctl", err)
		}

		scanner := bufio.NewScanner(outputPipe)
		for scanner.Scan() {
			fmt.Println(scanner.Text())
			select {
			case journal <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			die("bufio.Scanner failed", err)
		}
	}()

	// Wait for exepipe to start.
	for {
		var line string
		select {
		case line = <-journal:
		case <-time.After(10 * time.Second):
			die("waited too long for exepipe startup", nil)
		}
		if strings.Contains(line, "server started") {
			break
		}
	}

	client, err := client.NewClient(ctx, "@exepipe", slog.Default())
	if err != nil {
		die("NewClient failed", err)
	}

	// Set up a listener. Connections to externalListener
	// will be forwarded to internalListener.

	externalListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		die("net.Listen failed", err)
	}

	internalListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		die("net.Listen failed", err)
	}

	tcpAddr := internalListener.Addr().(*net.TCPAddr)
	if err := client.Listen(ctx, "key", externalListener, "", tcpAddr.IP.String(), tcpAddr.Port, "test"); err != nil {
		die("client.Listen failed", err)
	}

	// Receive connections on internalListener.
	internalServerChan := make(chan net.Conn, 2)
	go func() {
		for {
			internalServer, err := internalListener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					die("internalListener.Accept failed", err)
				}
				return
			}
			internalServerChan <- internalServer
		}
	}()

	// Open a connection to the external listener.
	externalClient, err := net.Dial(externalListener.Addr().Network(), externalListener.Addr().String())
	if err != nil {
		die("dialing external listener failed", err)
	}

	// exepipe should have routed that to open an internal server.
	var internalServer net.Conn
	select {
	case internalServer = <-internalServerChan:
	case <-time.After(10 * time.Second):
		die("timeout waiting for internal server", nil)
	}

	// Anything we write to externalClient should be sent to
	// internalServer, and vice-versa.
	testCopy("before restart", externalClient, internalServer)

	controllerPID, childPIDs := exepipePIDs(ctx)
	if controllerPID == 0 {
		die("no exepipe controller", nil)
	}
	if len(childPIDs) != 1 {
		die(fmt.Sprintf("got %d exepipe children, want 1", len(childPIDs)), nil)
	}

	// All the above was covered by exepipe/listen_test.go.
	// Now we get to the point of this test:
	// tell systemd to restart exepipe.
	cmd := exec.CommandContext(ctx, "sudo", "systemctl", "restart", "exepipe")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		die("systemctl restart failed", err)
	}

	// Wait for a new controller.
	tries := 0
	for {
		newPID, _ := exepipePIDs(ctx)
		if newPID != 0 && newPID != controllerPID {
			controllerPID = newPID
			break
		}

		tries++
		if tries > 100 {
			die("too many retries waiting for exepipe to restart", nil)
		}

		time.Sleep(time.Duration(tries) * time.Millisecond)
	}

	// Wait for all listeners to be transferred.
	for {
		var line string
		select {
		case line = <-journal:
		case <-time.After(10 * time.Second):
			die("waited too long for exepipe transfer message", nil)
		}
		if strings.Contains(line, "transferred all listeners") {
			break
		}
	}

	// Verify that listener still works.
	// This is expected to use the restarted exepipe.

	externalClient2, err := net.Dial(externalListener.Addr().Network(), externalListener.Addr().String())
	if err != nil {
		die("dialing external listener 2 failed", err)
	}

	var internalServer2 net.Conn
	select {
	case internalServer2 = <-internalServerChan:
	case <-time.After(10 * time.Second):
		die("timeout waiting for internal server", nil)
	}

	testCopy("second connection", externalClient2, internalServer2)

	// Verify that the original connection still works.
	// This is expected to use the original exepipe.

	_, newChildPIDs := exepipePIDs(ctx)
	if len(newChildPIDs) != 2 {
		die(fmt.Sprintf("got %d exepipe children, want 2", len(newChildPIDs)), nil)
	}

	testCopy("first connection after restart", externalClient, internalServer)

	// Close externalClient. That should cause the original exepipe
	// to stop copying, and to shut down. We intentionally
	// don't close internalServer as just closing externalClient
	// should be enough to stop exepipe.
	externalClient.Close()
	tries = 0
	for {
		_, newChildPIDs := exepipePIDs(ctx)
		if len(newChildPIDs) == 1 {
			if newChildPIDs[0] == childPIDs[0] {
				die("no new child exepipe", nil)
			}
			break
		}

		tries++
		if tries > 100 {
			die("too many retries waiting for child exepipe to exit", nil)
		}

		time.Sleep(time.Duration(tries) * time.Millisecond)
	}

	externalClient2.Close()
	internalServer2.Close()

	externalClient3, err := net.Dial(externalListener.Addr().Network(), externalListener.Addr().String())
	if err != nil {
		die("dialing external listener 3 failed", err)
	}

	var internalServer3 net.Conn
	select {
	case internalServer3 = <-internalServerChan:
	case <-time.After(10 * time.Second):
		die("timeout waiting for internal server", nil)
	}

	testCopy("second connection after restart", externalClient3, internalServer3)
}

// die exits the program with a message and an error
func die(msg string, err error) {
	if err == nil {
		fmt.Fprintln(os.Stderr, msg)
	} else {
		fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	}
	os.Exit(1)
}

// exepipePIDs returns the process IDs of exepipe processes.
// The first return is the controller PID,
// the second is the child PIDs.
func exepipePIDs(ctx context.Context) (int, []int) {
	var stdoutBuf, stderrBuf strings.Builder
	cmd := exec.CommandContext(ctx, "ps", "ax", "--format", "pid args")
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if stderrBuf.Len() > 0 {
		fmt.Fprintf(os.Stderr, "ps error output\n%s", stderrBuf)
		os.Exit(1)
	}
	if err != nil {
		die("ps failed", err)
	}

	stdout := stdoutBuf.String()
	var controllerPID int
	var childPIDs []int
	for line := range strings.SplitSeq(stdout, "\n") {
		if !strings.Contains(line, "exepipe") {
			continue
		}
		if strings.Contains(line, "exepipe_tester") || strings.Contains(line, "journalctl") {
			continue
		}

		pidStr := strings.Fields(line)[0]
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			die(fmt.Sprintf("can't parse PID %q", pidStr), err)
		}

		if strings.Contains(line, "-controller") {
			if controllerPID != 0 {
				fmt.Fprintf(os.Stderr, "ps output\n%s", stdout)
				die("multiple exepipe controllers", nil)
			}
			controllerPID = pid
		} else {
			childPIDs = append(childPIDs, pid)
		}
	}

	if len(childPIDs) > 2 {
		fmt.Fprintf(os.Stderr, "ps output\n%s", stdout)
		die("too many exepipe child processes", nil)
	}

	return controllerPID, childPIDs
}

// testCopy takes a pair of descriptors and tests that data is
// copied between them.
func testCopy(msg string, c1, c2 net.Conn) {
	const count = 1024

	fromBuf1 := make([]byte, count)
	rand.Read(fromBuf1) // never fails

	fromBuf2 := make([]byte, count)
	rand.Read(fromBuf2)

	n, err := c1.Write(fromBuf1)
	if n != count || err != nil {
		die(fmt.Sprintf("%s bad Write: count %d", msg, n), err)
	}

	n, err = c2.Write(fromBuf2)
	if n != count || err != nil {
		die(fmt.Sprintf("%s bad Write: count %d", msg, n), err)
	}

	toBuf1 := make([]byte, count)
	n, err = io.ReadFull(c1, toBuf1)
	if n != count || err != nil {
		die(fmt.Sprintf("%s bad Read: count %d", msg, n), err)
	}

	toBuf2 := make([]byte, count)
	n, err = io.ReadFull(c2, toBuf2)
	if n != count || err != nil {
		die(fmt.Sprintf("%s bad Read: count %d", msg, n), err)
	}

	if !bytes.Equal(fromBuf1, toBuf2) {
		die(fmt.Sprintf("%s bad copy: got %q want %q", msg, toBuf2, fromBuf1), nil)
	}
	if !bytes.Equal(fromBuf2, toBuf1) {
		die(fmt.Sprintf("%s bad copy: got %q want %q", msg, toBuf1, fromBuf2), nil)
	}
}
