package sshproxy

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server handles SSH connections and proxies them to containers
type Server struct {
	containerFS ContainerFS
	executor    ContainerExecutor
	homeDir     string
}

// NewServer creates a new SSH proxy server
func NewServer(fs ContainerFS, executor ContainerExecutor, homeDir string) *Server {
	return &Server{
		containerFS: fs,
		executor:    executor,
		homeDir:     homeDir,
	}
}

// HandleChannels processes SSH channel requests
func (s *Server) HandleChannels(ctx context.Context, channels <-chan ssh.NewChannel) {
	for newChannel := range channels {
		go s.handleChannel(ctx, newChannel)
	}
}

func (s *Server) handleChannel(ctx context.Context, newChannel ssh.NewChannel) {
	switch newChannel.ChannelType() {
	case "session":
		s.handleSession(ctx, newChannel)
	case "direct-tcpip":
		// Port forwarding - implement if needed
		newChannel.Reject(ssh.UnknownChannelType, "port forwarding not supported")
	default:
		newChannel.Reject(ssh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", newChannel.ChannelType()))
	}
}

func (s *Server) handleSession(ctx context.Context, newChannel ssh.NewChannel) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	
	// Process session requests
	for req := range requests {
		switch req.Type {
		case "exec":
			s.handleExec(ctx, channel, req)
			return // exec completes the session
			
		case "shell":
			s.handleShell(ctx, channel, req)
			return // shell completes the session
			
		case "pty-req":
			// Handle PTY request
			req.Reply(true, nil)
			
		case "env":
			// Handle environment variable
			req.Reply(true, nil)
			
		case "subsystem":
			if s.handleSubsystem(ctx, channel, req) {
				return // subsystem completes the session
			}
			
		default:
			req.Reply(false, nil)
		}
	}
}

func (s *Server) handleExec(ctx context.Context, channel ssh.Channel, req *ssh.Request) {
	// Parse exec command
	if len(req.Payload) < 4 {
		req.Reply(false, nil)
		return
	}
	
	cmdLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
	if len(req.Payload) < 4+cmdLen {
		req.Reply(false, nil)
		return
	}
	
	command := string(req.Payload[4 : 4+cmdLen])
	req.Reply(true, nil)
	
	// Parse the command
	args := parseCommand(command)
	if len(args) == 0 {
		channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
		return
	}
	
	// Special handling for SCP commands
	if args[0] == "scp" {
		s.handleSCP(ctx, channel, args)
		return
	}
	
	// Execute the command in the container
	err := s.executor.Execute(ctx, args, channel, channel, channel.Stderr())
	
	// Send exit status
	exitStatus := uint32(0)
	if err != nil {
		exitStatus = 1
	}
	channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitStatus}))
}

func (s *Server) handleShell(ctx context.Context, channel ssh.Channel, req *ssh.Request) {
	req.Reply(true, nil)
	
	// Start an interactive shell
	shell := []string{"/bin/sh", "-i"}
	err := s.executor.Execute(ctx, shell, channel, channel, channel.Stderr())
	
	// Send exit status
	exitStatus := uint32(0)
	if err != nil {
		exitStatus = 1
	}
	channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitStatus}))
}

func (s *Server) handleSubsystem(ctx context.Context, channel ssh.Channel, req *ssh.Request) bool {
	if len(req.Payload) < 4 {
		req.Reply(false, nil)
		return false
	}
	
	subsystemLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
	if len(req.Payload) < 4+subsystemLen {
		req.Reply(false, nil)
		return false
	}
	
	subsystem := string(req.Payload[4 : 4+subsystemLen])
	
	if subsystem == "sftp" {
		req.Reply(true, nil)
		s.handleSFTP(ctx, channel)
		return true
	}
	
	req.Reply(false, nil)
	return false
}

func (s *Server) handleSFTP(ctx context.Context, channel ssh.Channel) {
	// Create SFTP handler
	handler := NewSFTPHandler(ctx, s.containerFS, s.homeDir)
	
	// Create SFTP server
	handlers := sftp.Handlers{
		FileGet:  handler, // Read files
		FilePut:  handler, // Write files
		FileCmd:  handler, // File commands (mkdir, remove, etc.)
		FileList: handler, // List files
	}
	
	server := sftp.NewRequestServer(channel, handlers)
	
	// Serve SFTP requests
	if err := server.Serve(); err != nil && err != io.EOF {
		// Log error but don't send to channel (would break protocol)
		fmt.Printf("SFTP server error: %v\n", err)
	}
}

func (s *Server) handleSCP(ctx context.Context, channel ssh.Channel, args []string) {
	// Modern SCP uses SFTP protocol internally
	// We should not get here if the client is using modern OpenSSH
	// If we do, it means the client is using legacy SCP protocol
	
	// Send error message
	channel.Stderr().Write([]byte("This server only supports modern SCP (SFTP protocol). Please upgrade your SSH client.\n"))
	channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
}

// parseCommand splits a command string into arguments
func parseCommand(cmd string) []string {
	// Simple command parsing - doesn't handle quotes properly
	// For production, use a proper shell parser
	return strings.Fields(cmd)
}