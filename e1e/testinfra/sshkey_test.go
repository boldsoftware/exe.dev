package testinfra

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// sshCmd is the command we send via ssh.
const sshCmd = "date"

// sshReply is the reply sent by the fake ssh server.
const sshReply = "the time is now"

// sawCmd is set if the sshd version saw the command.
var sawCmd = false

func TestGenSSHKey(t *testing.T) {
	var wg sync.WaitGroup
	defer wg.Wait()

	path, publicKey, err := GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sshListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshListener.Close()

	wg.Go(func() {
		sshdServer(t, sshListener, path, publicKey)
	})

	cmd := exec.CommandContext(t.Context(),
		"ssh",
		"-F", "/dev/null",
		"-p", strconv.Itoa(sshListener.Addr().(*net.TCPAddr).Port),
		"-o", "IdentityFile="+path,
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		"test@localhost",
		sshCmd,
	)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := io.TeeReader(stdoutPipe, t.Output())
	cmd.Stderr = t.Output()
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start ssh: %v", err)
	}

	outputDone := make(chan bool)
	var output strings.Builder
	wg.Go(func() {
		defer close(outputDone)
		_, err := io.Copy(&output, stdout)
		if err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("error reading from ssh stdout: %v", err)
		}
	})

	if err := cmd.Wait(); err != nil {
		t.Errorf("ssh failed with exit status %v", err)
	}

	if !sawCmd {
		t.Error("ssh server did not see expected command")
	}

	<-outputDone
	if !strings.Contains(output.String(), sshReply) {
		t.Errorf("ssh output does not contain expected string %q", sshReply)
	}
}

// sshdServer runs a test SSH server.
func sshdServer(t *testing.T, ln net.Listener, keyFileServer, publicKeyServer string) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		c, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Error(err)
			}
			return
		}

		wg.Go(func() {
			sshdServerConn(t, c, keyFileServer, publicKeyServer)
		})
	}
}

// sshdServerConn handles a single connection to the test SSH server.
func sshdServerConn(t *testing.T, c net.Conn, keyFileServer, publicKeyServer string) {
	defer c.Close()

	t.Logf("sshdServer connection from %s", c.RemoteAddr())

	keyFileData, err := os.ReadFile(keyFileServer)
	if err != nil {
		t.Error(err)
		return
	}
	hostKeyParsed, err := ssh.ParseRawPrivateKey(keyFileData)
	if err != nil {
		t.Error(err)
		return
	}
	hostKey, err := ssh.NewSignerFromKey(hostKeyParsed)
	if err != nil {
		t.Error(err)
		return
	}

	sshdConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			t.Logf("sshd PublicKeyCallback with key %q", key)
			sshKey := ssh.MarshalAuthorizedKey(key)
			if strings.TrimSpace(string(sshKey)) == strings.TrimSpace(publicKeyServer) {
				return nil, nil
			}
			t.Errorf("sshd server key mismatch got %q want %q", sshKey, publicKeyServer)
			return nil, fmt.Errorf("got key %s, want %s", sshKey, publicKeyServer)
		},
	}
	sshdConfig.AddHostKey(hostKey)

	sshdConn, channels, requests, err := ssh.NewServerConn(c, sshdConfig)
	if err != nil {
		t.Errorf("sshd server failure: %v", err)
	}
	defer sshdConn.Close()

	for channels != nil || requests != nil {
		select {
		case req, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			t.Logf("sshd request %v", req.Type)
			if err := req.Reply(req.Type == "shell", nil); err != nil {
				t.Errorf("sshd req.Reply error %v", err)
			}
		case newChannel, ok := <-channels:
			if !ok {
				channels = nil
				continue
			}
			t.Logf("new channel type %q data %q", newChannel.ChannelType(), newChannel.ExtraData())
			channel, channelReqs, err := newChannel.Accept()
			if err != nil {
				t.Errorf("sshd server new channel error: %v", err)
				continue
			}
			handleSSHDChannel(t, channel, channelReqs)
		}
	}
}

// handleSSHDChannel handles a new sshd channel.
// We only expect a single command.
func handleSSHDChannel(t *testing.T, channel ssh.Channel, channelReqs <-chan *ssh.Request) {
	t.Log("ssh channel opened")

	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	go func() {
		defer wg.Done()

		for {
			buf := make([]byte, 100)
			n, err := channel.Read(buf)
			if err != nil {
				if err != io.EOF {
					t.Errorf("sshd channel read error: %v", err)
				}
				return
			}

			t.Logf("sshd received on channel: %q", buf[:n])
		}
	}()

	for req := range channelReqs {
		t.Logf("sshd channel request %q payload %q", req.Type, req.Payload)
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var cmd struct {
			Command string
		}
		if err := ssh.Unmarshal(req.Payload, &cmd); err != nil {
			t.Errorf("sshd failed to unmarshal channel request: %v", err)
			req.Reply(false, nil)
			continue
		}

		if cmd.Command != sshCmd {
			t.Errorf("sshd got command %q expected %q", cmd.Command, sshCmd)
			req.Reply(false, nil)
			continue
		}

		if err := req.Reply(true, nil); err != nil {
			t.Errorf("sshd channel req.Reply error %v", err)
		}

		if _, err := channel.Write([]byte(sshReply + "\n")); err != nil {
			t.Errorf("sshd channel write error %v", err)
		}

		sawCmd = true

		type status struct {
			Status uint32
		}
		msg := status{
			Status: 0,
		}
		if _, err := channel.SendRequest("exit-status", false, ssh.Marshal(&msg)); err != nil {
			t.Errorf("sshd channel exit-status request error %v", err)
		}

		if err := channel.Close(); err != nil {
			t.Errorf("sshd channel close error %v", err)
		}
	}

	if !sawCmd {
		t.Error("sshd channel did not see expected command")
	}
}
