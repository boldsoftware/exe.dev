package testinfra

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tg123/sshpiper/libplugin"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
)

// sshCmd is the command we send via ssh.
const sshCmd = "date"

// sawCmd is set if the sshd version saw the command.
var sawCmd = false

func TestSSHPiperd(t *testing.T) {
	var wg sync.WaitGroup
	defer wg.Wait()

	// The SSH key we use to connect to sshpiperd.
	keyFileClient, publicKeyClient := genSSHKey(t)

	// The SSH key we use to connect to our sshd server.
	keyFileServer, publicKeyServer := genSSHKey(t)

	grpcListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Error(err)
		return
	}
	defer grpcListener.Close()

	sshListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Error(err)
		return
	}
	defer sshListener.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		sshPiperdPluginServer(t, grpcListener, sshListener.Addr(), publicKeyClient, keyFileServer)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		sshdServer(t, sshListener, keyFileServer, publicKeyServer)
	}()

	pi, err := StartSSHPiperd(t.Context(), grpcListener.Addr().(*net.TCPAddr).Port, t.Output())
	if err != nil {
		t.Error(err)
		return
	}

	cmd := exec.CommandContext(t.Context(),
		"ssh",
		"-F", "/dev/null",
		"-p", strconv.Itoa(pi.Port),
		"-o", "IdentityFile="+keyFileClient,
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		"test@localhost",
		sshCmd,
	)
	cmd.Stdout = t.Output()
	cmd.Stderr = t.Output()
	if err := cmd.Run(); err != nil {
		t.Errorf("ssh exit status: %v", err)
	}

	if !sawCmd {
		t.Error("ssh server did not see expected command")
	}

	pi.Stop(t.Context())
	<-pi.Exited
}

// sshPiperdPluginServer runs a test sshpiperd grpc plugin.
func sshPiperdPluginServer(t *testing.T, ln net.Listener, sshdAddr net.Addr, publicKeyClient, keyFileServer string) {
	config := libplugin.SshPiperPluginConfig{
		PublicKeyCallback: func(conn libplugin.ConnMetadata, key []byte) (*libplugin.Upstream, error) {
			return publicKeyCallback(t, conn, key, sshdAddr, publicKeyClient, keyFileServer)
		},
	}
	s := grpc.NewServer()
	plugin, err := libplugin.NewFromGrpc(config, s, ln)
	if err != nil {
		t.Errorf("libplugin grpc server start failed: %v", err)
		return
	}
	if err := plugin.Serve(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("libplugin grpc server: %v", err)
	}
}

// publicKeyCallback handles the sshpiper PublicKeyCallback.
// We tell sshpiper to redirect to the test ssh server.
func publicKeyCallback(t *testing.T, conn libplugin.ConnMetadata, key []byte, sshdAddr net.Addr, publicKeyClient, keyFileServer string) (*libplugin.Upstream, error) {
	sshPublicKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		t.Errorf("ParsePublicKey error %v", err)
	} else {
		sshKey := ssh.MarshalAuthorizedKey(sshPublicKey)
		if strings.TrimSpace(string(sshKey)) != strings.TrimSpace(publicKeyClient) {
			t.Errorf("sshpiperd plugin callback: got key %q, expected %q", sshKey, publicKeyClient)
		}
	}

	keyServer, err := os.ReadFile(keyFileServer)
	if err != nil {
		return nil, err
	}

	sshdTCPAddr := sshdAddr.(*net.TCPAddr)
	upstream := &libplugin.Upstream{
		Host:          sshdTCPAddr.IP.String(),
		Port:          int32(sshdTCPAddr.Port),
		UserName:      "test",
		IgnoreHostKey: true,
		Auth:          libplugin.CreatePrivateKeyAuth(keyServer),
	}
	return upstream, nil
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

		wg.Add(1)
		go func() {
			defer wg.Done()
			sshdServerConn(t, c, keyFileServer, publicKeyServer)
		}()
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

		if _, err := channel.Write([]byte(time.Now().String() + "\n")); err != nil {
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

// genSSHKey generates an SSH key for a test.
// It returns a path to a file holding the private key,
// and the public key as a string.
func genSSHKey(t *testing.T) (path, publickey string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}

	privKeyPath := filepath.Join(t.TempDir(), "id_ed25519")
	privKeyFile, err := os.OpenFile(privKeyPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("failed to create private key file: %v", err)
	}
	privateKeyBytes, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatalf("Failed to marshal private key: %v", err)
	}
	if err := pem.Encode(privKeyFile, privateKeyBytes); err != nil {
		t.Fatalf("Failed to write private key: %v", err)
	}
	err = privKeyFile.Close()
	if err != nil {
		t.Fatalf("failed to close private key file: %v", err)
	}

	publicKey := privateKey.Public().(ed25519.PublicKey)
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	pubStr := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPublicKey)), "\n")
	return privKeyPath, pubStr
}
