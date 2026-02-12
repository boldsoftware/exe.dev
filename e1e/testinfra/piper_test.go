package testinfra

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tg123/sshpiper/libplugin"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
)

func TestSSHPiperd(t *testing.T) {
	var wg sync.WaitGroup
	defer wg.Wait()

	// The SSH key we use to connect to sshpiperd.
	keyFileClient, publicKeyClient, err := GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The SSH key we use to connect to our sshd server.
	keyFileServer, publicKeyServer, err := GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	grpcListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcListener.Close()

	sshListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshListener.Close()

	wg.Go(func() {
		sshPiperdPluginServer(t, grpcListener, sshListener.Addr(), publicKeyClient, keyFileServer)
	})

	wg.Go(func() {
		sshdServer(t, sshListener, keyFileServer, publicKeyServer)
	})

	pi, err := StartSSHPiperd(t.Context(), grpcListener.Addr().(*net.TCPAddr).Port, t.Output())
	if err != nil {
		t.Fatal(err)
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
	go func() {
		defer close(outputDone)
		_, err := io.Copy(&output, stdout)
		if err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("error reading from ssh stdout: %v", err)
		}
	}()

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
