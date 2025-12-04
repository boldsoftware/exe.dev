// Copyright 2014 Boshi Lian<farmer1992@gmail.com>. All rights reserved.
// this file is governed by MIT-license
//
// https://github.com/tg123/sshpiper
package ssh

import (
	"errors"
	"fmt"
	"net"
	"slices"
)

type Upstream struct {
	Conn net.Conn

	Address string

	ClientConfig
}

// ChallengeContext represents the context for an authentication challenge.
// It provides methods to retrieve metadata and the username being challenged.
type ChallengeContext interface {

	// Meta returns the metadata associated with the challenge.
	// This can be used to store and retrieve additional information
	// related to the authentication process.
	//
	Meta() interface{}

	// ChallengedUsername returns the username challenged.
	ChallengedUsername() string
}

// PiperConfig holds SSHPiper specific configuration data.
// PiperConfig represents the configuration for the SSH piper.
type PiperConfig struct {
	Config

	// PublicKeyAuthAlgorithms specifies the supported client public key
	// authentication algorithms. Note that this should not include certificate
	// types since those use the underlying algorithm. This list is sent to the
	// client if it supports the server-sig-algs extension. Order is irrelevant.
	// If unspecified then a default set of algorithms is used.
	PublicKeyAuthAlgorithms []string

	// MaxAuthTries specifies the maximum number of authentication attempts
	// permitted per connection. If set to a negative number, the number of
	// attempts are unlimited. If set to zero, the number of attempts are limited
	// to 6.
	MaxAuthTries int

	// ServerVersion is the version identification string to announce in the public handshake.
	// If empty, a reasonable default is used.
	// Note that RFC 4253 section 4.2 requires that this string start with "SSH-2.0-".
	ServerVersion string

	hostKeys []Signer

	// CreateChallengeContext, if non-nil, that creates a challenge context for the connection metadata.
	CreateChallengeContext func(downconn ServerPreAuthConn) (ChallengeContext, error)

	// NextAuthMethods, if non-nil, that returns the next authentication methods to be used.
	NextAuthMethods func(downconn ConnMetadata, challengeCtx ChallengeContext) ([]string, error)

	// NoClientAuthCallback, if non-nil, that is called when the downstream requests a none auth.
	NoClientAuthCallback func(downconn ConnMetadata, challengeCtx ChallengeContext) (*Upstream, error)

	// PasswordCallback, if non-nil, that is called when the downstream requests a password auth.
	// It returns the upstream connection and an error.
	PasswordCallback func(downconn ConnMetadata, password []byte, challengeCtx ChallengeContext) (*Upstream, error)

	// PublicKeyCallback, if non-nil, that is called when the downstream requests a publickey auth.
	// It returns the upstream connection and an error.
	PublicKeyCallback func(downconn ConnMetadata, key PublicKey, challengeCtx ChallengeContext) (*Upstream, error)

	// KeyboardInteractiveCallback, if non-nil, that is called when the downstream requests a keyboard interactive auth.
	// It returns the upstream connection and an error.
	KeyboardInteractiveCallback func(downconn ConnMetadata, client KeyboardInteractiveChallenge, challengeCtx ChallengeContext) (*Upstream, error)

	// UpstreamAuthFailureCallback, if non-nil, that is called when the upstream authentication fails.
	UpstreamAuthFailureCallback func(downconn ConnMetadata, method string, err error, challengeCtx ChallengeContext)

	// DownstreamBannerCallback, if non-nil, that is called after key exchange completed but before authentication.
	// It returns the banner string to be sent to the client.
	DownstreamBannerCallback func(downconn ConnMetadata, challengeCtx ChallengeContext) string

	// UpstreamBannerCallback, if non-nil, that is called after upstream sends banner but before authentication.
	// the default behavior is to send the banner to the downstream if not set.
	UpstreamBannerCallback func(downconn ServerPreAuthConn, banner string, challengeCtx ChallengeContext) error
}

// AddHostKey adds a private key as a SSHPiper host key. If an existing host
// key exists with the same algorithm, it is overwritten. Each SSHPiper
// config must have at least one host key.
func (s *PiperConfig) AddHostKey(key Signer) {
	for i, k := range s.hostKeys {
		if k.PublicKey().Type() == key.PublicKey().Type() {
			s.hostKeys[i] = key
			return
		}
	}

	s.hostKeys = append(s.hostKeys, key)
}

type upstream struct{ *connection }
type downstream struct{ *connection }

// PiperConn is a piped SSH connection, linking upstream ssh server and
// downstream ssh client together. After the piped connection was created,
// The downstream ssh client is authenticated by upstream ssh server and
// AdditionalChallenge from SSHPiper.
type PiperConn struct {
	upstream   *upstream
	downstream *downstream

	config         *PiperConfig
	authOnlyConfig *ServerConfig
	challengeCtx   ChallengeContext

	maxAuthTries int
	authFailures int
}

// Wait blocks until the piped connection has shut down, and returns the
// error causing the shutdown.
func (p *PiperConn) Wait() error {
	return p.WaitWithHook(nil, nil)
}

// PipePacketHookMethod defines how the hook should handle the packet.
type PipePacketHookMethod int

const (
	// PipePacketHookTransform means the hook return transformed packet
	// to the original packet.
	// The original packet will be ignored and not sent to the other side.
	// The transformed packet will be sent to the other side.
	PipePacketHookTransform PipePacketHookMethod = iota

	// PipePacketHookReply means the hook return a reply packet
	// to the original packet.
	// The original packet will be ignored and not sent to the other side.
	// The reply packet will be sent to the other side.
	PipePacketHookReply
)

// PipePacketHook is a hook function that is called when a packet is received
// from the upstream or downstream connection. It allows you to modify the
// packet before it is sent to the other side.
// The hook function should return the method to be used and the modified
// packet. The method can be one of the following:
//   - PipePacketHookTransform: the packet is transformed and sent to the
//     other side.
//   - PipePacketHookReply: the packet is a reply to the original packet
//     and should be sent to the other side.
//
// If the hook function returns an error, the piped connection will be closed
// and the error will be returned to the caller.
// If the hook function returns nil, the packet will be dropped
type PipePacketHook func(msg []byte) (PipePacketHookMethod, []byte, error)

// PingPacketReply is a PipePacketHook that replies to ping@openssh packets
// with a pong packet.
// This is useful when upstream does not support ping@openssh
// sshpiper will reply instead of crashing upstream
func PingPacketReply(packet []byte) (PipePacketHookMethod, []byte, error) {
	if packet[0] == msgPing {
		var msg pingMsg
		if err := Unmarshal(packet, &msg); err != nil {
			return PipePacketHookTransform, nil, fmt.Errorf("failed to unmarshal ping@openssh.com message: %w", err)
		}

		return PipePacketHookReply, Marshal(pongMsg(msg)), nil
	}
	return PipePacketHookTransform, packet, nil
}

// InspectPacketHook is a PipePacketHook that inspects the packet and
// inspect func should not modify the packet.
func InspectPacketHook(inspect func(msg []byte) error) PipePacketHook {
	if inspect == nil {
		return nil
	}

	return func(msg []byte) (PipePacketHookMethod, []byte, error) {
		if err := inspect(msg); err != nil {
			return PipePacketHookTransform, nil, err
		}

		return PipePacketHookTransform, msg, nil
	}
}

// WaitWithHook blocks until the piped connection has shut down, and returns the
// error causing the shutdown. It also allows you to specify hooks for
// upstream and downstream data. The hooks are called with the data read from
// the connection, and should return the data to be written to the connection.
//
// uphook is called with the data read from the upstream connection before sending to
// downstream
// downhook is called with the data read from the downstream connection before sending to
// upstream
func (p *PiperConn) WaitWithHook(uphook, downhook PipePacketHook) error {
	c := make(chan error, 2)

	if downhook != nil {
		go func() {
			c <- pipingWithHook(p.upstream.transport, p.downstream.transport, downhook)
		}()
	} else {
		go func() {
			c <- piping(p.upstream.transport, p.downstream.transport)
		}()
	}

	if uphook != nil {
		go func() {
			c <- pipingWithHook(p.downstream.transport, p.upstream.transport, uphook)
		}()
	} else {
		go func() {
			c <- piping(p.downstream.transport, p.upstream.transport)
		}()
	}

	defer p.Close()

	// wait until either connection closed
	return <-c
}

// Close the piped connection create by SSHPiper
func (p *PiperConn) Close() {
	p.upstream.transport.Close()
	p.downstream.transport.Close()
}

// UpstreamConnMeta returns the ConnMetadata of the piper and upstream
func (p *PiperConn) UpstreamConnMeta() ConnMetadata {
	return p.upstream
}

// DownstreamConnMeta returns the ConnMetadata of the piper and downstream
func (p *PiperConn) DownstreamConnMeta() ConnMetadata {
	return p.downstream
}

// ChallengeContext returns the ChallengeContext of the piper
func (p *PiperConn) ChallengeContext() ChallengeContext {
	return p.challengeCtx
}

func (p *PiperConn) mapToUpstreamViaDownstreamAuth() error {
	if err := p.updateAuthMethods(fmt.Errorf("no more auth methods")); err != nil {
		_, ok := err.(*PartialSuccessError)
		if !ok {
			return err
		}
	}

	if _, err := p.downstream.serverAuthenticate(p.authOnlyConfig); err != nil {
		return err
	}

	return nil
}

func (p *PiperConn) authUpstream(downstream ConnMetadata, method string, upstream *Upstream) error {
	if upstream == nil {
		return p.updateAuthMethods(fmt.Errorf("empty upstream"))
	}

	if upstream.User == "" {
		upstream.User = downstream.User()
	}

	config := &upstream.ClientConfig
	addr := upstream.Address

	origBannerCallback := config.BannerCallback
	config.BannerCallback = func(message string) error {
		if origBannerCallback != nil {
			if err := origBannerCallback(message); err != nil {
				return err
			}
		}

		if p.config.UpstreamBannerCallback != nil {

			preauth, ok := downstream.(ServerPreAuthConn)
			if ok {
				return p.config.UpstreamBannerCallback(preauth, message, p.challengeCtx)
			}
		}

		return p.downstream.SendAuthBanner(message)
	}

	u, err := newUpstream(upstream.Conn, addr, config)
	if err != nil {
		return err
	}

	if err := u.clientAuthenticateReturnAllowed(config); err != nil {
		if p.config.UpstreamAuthFailureCallback != nil {
			p.config.UpstreamAuthFailureCallback(downstream, method, err, p.challengeCtx)
		}

		p.authFailures++

		return p.updateAuthMethods(err)
	}

	u.user = config.User
	p.upstream = u

	return nil
}

func (p *PiperConn) noClientAuthCallback(conn ConnMetadata) (*Permissions, error) {
	u, err := p.config.NoClientAuthCallback(conn, p.challengeCtx)
	if err != nil {
		p.authFailures++
		return nil, p.updateAuthMethods(err)
	}

	return nil, p.authUpstream(conn, "none", u)
}

func (p *PiperConn) passwordCallback(conn ConnMetadata, password []byte) (*Permissions, error) {
	u, err := p.config.PasswordCallback(conn, password, p.challengeCtx)
	if err != nil {
		p.authFailures++
		return nil, p.updateAuthMethods(err)
	}

	return nil, p.authUpstream(conn, "password", u)
}

func (p *PiperConn) publicKeyCallback(conn ConnMetadata, key PublicKey) (*Permissions, error) {
	u, err := p.config.PublicKeyCallback(conn, key, p.challengeCtx)
	if err != nil {
		p.authFailures++
		return nil, p.updateAuthMethods(err)
	}

	return nil, p.authUpstream(conn, "publickey", u)
}

func (p *PiperConn) keyboardInteractiveCallback(conn ConnMetadata, client KeyboardInteractiveChallenge) (*Permissions, error) {
	u, err := p.config.KeyboardInteractiveCallback(conn, client, p.challengeCtx)
	if err != nil {
		p.authFailures++
		return nil, p.updateAuthMethods(err)
	}

	return nil, p.authUpstream(conn, "keyboard-interactive", u)
}

func (p *PiperConn) downstreamBannerCallback(conn ConnMetadata) string {
	return p.config.DownstreamBannerCallback(conn, p.challengeCtx)
}

func (p *PiperConn) updateAuthMethods(emptyerr error) error {

	if p.authFailures > p.maxAuthTries && p.maxAuthTries >= 0 {
		return emptyerr
	}

	authMethods := []string{"none", "password", "publickey", "keyboard-interactive"}
	if p.config.NextAuthMethods != nil {
		var err error
		authMethods, err = p.config.NextAuthMethods(p.downstream, p.challengeCtx)
		if err != nil {
			return err
		}
	}

	p.authOnlyConfig.NoClientAuthCallback = nil
	p.authOnlyConfig.PasswordCallback = nil
	p.authOnlyConfig.PublicKeyCallback = nil
	p.authOnlyConfig.KeyboardInteractiveCallback = nil

	for _, authMethod := range authMethods {
		switch authMethod {
		case "none":
			if p.config.NoClientAuthCallback != nil {
				p.authOnlyConfig.NoClientAuthCallback = p.noClientAuthCallback
				p.authOnlyConfig.NoClientAuth = true
			}
		case "password":
			if p.config.PasswordCallback != nil {
				p.authOnlyConfig.PasswordCallback = p.passwordCallback
			}
		case "publickey":
			if p.config.PublicKeyCallback != nil {
				p.authOnlyConfig.PublicKeyCallback = p.publicKeyCallback
			}
		case "keyboard-interactive":
			if p.config.KeyboardInteractiveCallback != nil {
				p.authOnlyConfig.KeyboardInteractiveCallback = p.keyboardInteractiveCallback
			}
		}
	}

	if len(authMethods) > 0 {
		return &PartialSuccessError{
			Next: ServerAuthCallbacks{
				PasswordCallback:            p.authOnlyConfig.PasswordCallback,
				PublicKeyCallback:           p.authOnlyConfig.PublicKeyCallback,
				KeyboardInteractiveCallback: p.authOnlyConfig.KeyboardInteractiveCallback,
			},
		}
	}

	return emptyerr
}

// NewSSHPiperConn starts a piped ssh connection witch conn as its downstream transport.
// It handshake with downstream ssh client and upstream ssh server provicde by FindUpstream.
// If either handshake is unsuccessful, the whole piped connection will be closed.
func NewSSHPiperConn(conn net.Conn, config *PiperConfig) (*PiperConn, error) {
	d, err := newDownstream(conn, &ServerConfig{
		Config:                  config.Config,
		hostKeys:                config.hostKeys,
		ServerVersion:           config.ServerVersion,
		PublicKeyAuthAlgorithms: config.PublicKeyAuthAlgorithms,
	})
	if err != nil {
		return nil, err
	}

	p := &PiperConn{
		downstream: d,
		config:     config,
		authOnlyConfig: &ServerConfig{
			MaxAuthTries:            -1,
			PublicKeyAuthAlgorithms: supportedPubKeyAuthAlgos,
		},
		authFailures: 0,
	}

	if config.MaxAuthTries == 0 {
		p.maxAuthTries = 6
	}

	if config.CreateChallengeContext != nil {
		ctx, err := config.CreateChallengeContext(d)
		if err != nil {
			return nil, err
		}
		p.challengeCtx = ctx
	}

	if config.DownstreamBannerCallback != nil {
		p.authOnlyConfig.BannerCallback = p.downstreamBannerCallback
	}

	if err := p.mapToUpstreamViaDownstreamAuth(); err != nil {
		return nil, err
	}

	return p, nil
}

func piping(dst, src packetConn) error {
	for {
		p, err := src.readPacket()
		if err != nil {
			return err
		}

		err = dst.writePacket(p)
		if err != nil {
			return err
		}
	}
}

func pipingWithHook(dst, src packetConn, hook PipePacketHook) error {
	for {
		original, err := src.readPacket()
		if err != nil {
			return err
		}

		method, hooked, err := hook(original)
		if err != nil {
			return err
		}

		if hooked == nil {
			continue
		}

		switch method {
		case PipePacketHookTransform:
			err = dst.writePacket(hooked)
			if err != nil {
				return err
			}
		case PipePacketHookReply:
			if err := src.writePacket(hooked); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown hook method: %d", method)
		}
	}
}

// NoneAuth returns an AuthMethod that represents "none" authentication.
// This method is typically used to indicate that no authentication is required
// or to test if the server allows unauthenticated access.
func NoneAuth() AuthMethod {
	return new(noneAuth)
}

// ---------------------------------------------------------------------------------------------------------------------
// below are copy and modified ssh code
// ---------------------------------------------------------------------------------------------------------------------

func newDownstream(c net.Conn, config *ServerConfig) (*downstream, error) {
	fullConf := *config
	fullConf.SetDefaults()

	if len(fullConf.PublicKeyAuthAlgorithms) == 0 {
		fullConf.PublicKeyAuthAlgorithms = defaultPubKeyAuthAlgos
	} else {
		for _, algo := range fullConf.PublicKeyAuthAlgorithms {
			if !slices.Contains(SupportedAlgorithms().PublicKeyAuths, algo) && !slices.Contains(InsecureAlgorithms().PublicKeyAuths, algo) {
				c.Close()
				return nil, fmt.Errorf("ssh: unsupported public key authentication algorithm %s", algo)
			}
		}
	}

	s := &connection{
		sshConn: sshConn{conn: c},
	}

	_, err := s.serverHandshakeNoAuth(&fullConf)
	if err != nil {
		c.Close()
		return nil, err
	}

	return &downstream{s}, nil
}

func newUpstream(c net.Conn, addr string, config *ClientConfig) (*upstream, error) {
	fullConf := *config
	fullConf.SetDefaults()
	if fullConf.HostKeyCallback == nil {
		c.Close()
		return nil, errors.New("ssh: must specify HostKeyCallback")
	}

	conn := &connection{
		sshConn: sshConn{conn: c},
	}

	if err := conn.clientHandshakeNoAuth(addr, &fullConf); err != nil {
		c.Close()
		return nil, fmt.Errorf("ssh: handshake failed: %v", err)
	}

	return &upstream{conn}, nil
}

func (c *connection) clientHandshakeNoAuth(dialAddress string, config *ClientConfig) error {
	c.clientVersion = []byte(packageVersion)
	if config.ClientVersion != "" {
		c.clientVersion = []byte(config.ClientVersion)
	}

	var err error
	c.serverVersion, err = exchangeVersions(c.sshConn.conn, c.clientVersion)
	if err != nil {
		return err
	}

	c.transport = newClientTransport(
		newTransport(c.sshConn.conn, config.Rand, true /* is client */),
		c.clientVersion, c.serverVersion, config, dialAddress, c.sshConn.RemoteAddr())

	if err := c.transport.waitSession(); err != nil {
		return err
	}

	c.sessionID = c.transport.getSessionID()
	return nil
}

func (c *connection) serverHandshakeNoAuth(config *ServerConfig) (*Permissions, error) {
	if len(config.hostKeys) == 0 {
		return nil, errors.New("ssh: server has no host keys")
	}

	var err error
	if config.ServerVersion != "" {
		c.serverVersion = []byte(config.ServerVersion)
	} else {
		c.serverVersion = []byte("SSH-2.0-SSHPiper")
	}
	c.clientVersion, err = exchangeVersions(c.sshConn.conn, c.serverVersion)
	if err != nil {
		return nil, err
	}

	tr := newTransport(c.sshConn.conn, config.Rand, false /* not client */)
	c.transport = newServerTransport(tr, c.clientVersion, c.serverVersion, config)

	if err := c.transport.waitSession(); err != nil {
		return nil, err

	}
	c.sessionID = c.transport.getSessionID()

	var packet []byte
	if packet, err = c.transport.readPacket(); err != nil {
		return nil, err
	}

	var serviceRequest serviceRequestMsg
	if err = Unmarshal(packet, &serviceRequest); err != nil {
		return nil, err
	}
	if serviceRequest.Service != serviceUserAuth {
		return nil, errors.New("ssh: requested service '" + serviceRequest.Service + "' before authenticating")
	}
	serviceAccept := serviceAcceptMsg{
		Service: serviceUserAuth,
	}
	if err := c.transport.writePacket(Marshal(&serviceAccept)); err != nil {
		return nil, err
	}

	return nil, nil
}

type NoMoreMethodsErr struct {
	Tried   []string
	Allowed []string
}

func (e NoMoreMethodsErr) Error() string {
	return fmt.Sprintf("ssh: unable to authenticate, attempted methods %v, no supported methods remain, allowed methods %v", e.Tried, e.Allowed)
}

func (c *connection) clientAuthenticateReturnAllowed(config *ClientConfig) error {
	// initiate user auth session
	if err := c.transport.writePacket(Marshal(&serviceRequestMsg{serviceUserAuth})); err != nil {
		return err
	}
	packet, err := c.transport.readPacket()
	if err != nil {
		return err
	}
	// The server may choose to send a SSH_MSG_EXT_INFO at this point (if we
	// advertised willingness to receive one, which we always do) or not. See
	// RFC 8308, Section 2.4.
	extensions := make(map[string][]byte)
	if len(packet) > 0 && packet[0] == msgExtInfo {
		var extInfo extInfoMsg
		if err := Unmarshal(packet, &extInfo); err != nil {
			return err
		}
		payload := extInfo.Payload
		for i := uint32(0); i < extInfo.NumExtensions; i++ {
			name, rest, ok := parseString(payload)
			if !ok {
				return parseError(msgExtInfo)
			}
			value, rest, ok := parseString(rest)
			if !ok {
				return parseError(msgExtInfo)
			}
			extensions[string(name)] = value
			payload = rest
		}
		packet, err = c.transport.readPacket()
		if err != nil {
			return err
		}
	}
	var serviceAccept serviceAcceptMsg
	if err := Unmarshal(packet, &serviceAccept); err != nil {
		return err
	}

	// during the authentication phase the client first attempts the "none" method
	// then any untried methods suggested by the server.
	var tried []string
	var lastMethods []string

	sessionID := c.transport.getSessionID()
	for auth := AuthMethod(new(noneAuth)); auth != nil; {
		ok, methods, err := auth.auth(sessionID, config.User, c.transport, config.Rand, extensions)
		if err != nil {
			return err
		}
		if ok == authSuccess {
			// success
			return nil
		} else if ok == authFailure {
			if m := auth.method(); !slices.Contains(tried, m) {
				tried = append(tried, m)
			}
		}
		if methods == nil {
			methods = lastMethods
		}
		lastMethods = methods

		auth = nil

	findNext:
		for _, a := range config.Auth {
			candidateMethod := a.method()
			if slices.Contains(tried, candidateMethod) {
				continue
			}
			for _, meth := range methods {
				if meth == candidateMethod {
					auth = a
					break findNext
				}
			}
		}
	}
	return NoMoreMethodsErr{Tried: tried, Allowed: lastMethods}
}
