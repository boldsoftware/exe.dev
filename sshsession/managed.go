package sshsession

import (
	gliderssh "github.com/gliderlabs/ssh"
)

// Session represents the managed session capabilities required by the SSH server.
type Session interface {
	gliderssh.Session
}
