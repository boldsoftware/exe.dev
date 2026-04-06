package libplugin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/tg123/remotesigner/grpcsigner"
	"github.com/tg123/sshpiper/libplugin/ioconn"
	"google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// AuthDenialError is returned by auth callbacks to explicitly deny an auth
// attempt and show the user a banner explaining why. sshpiperd surfaces the
// banner via SSH_MSG_USERAUTH_BANNER before the failure reply, then ends the
// connection without offering any fallback auth methods.
//
// Plugins should return *AuthDenialError (rather than a generic error) when
// they want the user to see a specific reason for the failure, e.g. "VM is
// not running". Returning any other error falls back to the default auth
// failure with no banner.
//
// The type is plugin-side only. At the gRPC boundary, the libplugin server
// translates a returned *AuthDenialError into the AuthDenial proto field;
// sshpiperd's plugin client then turns that field into a *ssh.AuthDenial,
// which sshpiperd's auth wrappers handle by sending the banner and failing
// the connection.
type AuthDenialError struct {
	// Banner is shown to the user via SSH_MSG_USERAUTH_BANNER. If empty,
	// nothing is shown to the user; the denial is still effective.
	Banner string
	// Reason is logged server-side but never shown to the client.
	Reason string
}

func (e *AuthDenialError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return "auth denied by plugin"
}

// Deny is a convenience constructor for an *AuthDenialError. Returning the
// result from an auth callback tells sshpiperd to reject the attempt, show
// the user the given banner via SSH_MSG_USERAUTH_BANNER, and end the
// connection without offering any fallback auth methods. The reason is
// logged server-side and never sent to the client.
func Deny(banner, reason string) error {
	return &AuthDenialError{Banner: banner, Reason: reason}
}

// asDenial returns the AuthDenial proto for a callback error if the error is
// (or wraps) an *AuthDenialError, along with true. Otherwise it returns nil, false.
func asDenial(err error) (*AuthDenial, bool) {
	var de *AuthDenialError
	if errors.As(err, &de) {
		return &AuthDenial{
			Banner: de.Banner,
			Reason: de.Reason,
		}, true
	}
	return nil, false
}

type ConnMetadata interface {
	User() string

	RemoteAddr() string

	LocalAddress() string

	UniqueID() string

	GetMeta(key string) string
}

func (c *ConnMeta) User() string {
	return c.UserName
}

func (c *ConnMeta) RemoteAddr() string {
	return c.FromAddr
}

func (c *ConnMeta) LocalAddress() string {
	return c.GetLocalAddr()
}

func (c *ConnMeta) UniqueID() string {
	return c.UniqId
}

func (c *ConnMeta) GetMeta(key string) string {
	return c.Metadata[key]
}

type KeyboardInteractiveChallenge func(user, instruction string, question string, echo bool) (answer string, err error)

type SshPiperPluginConfig struct {
	NewConnectionCallback func(conn ConnMetadata) error

	NextAuthMethodsCallback func(conn ConnMetadata) ([]string, error)

	NoClientAuthCallback func(conn ConnMetadata) (*Upstream, error)

	PasswordCallback func(conn ConnMetadata, password []byte) (*Upstream, error)

	PublicKeyCallback func(conn ConnMetadata, key []byte) (*Upstream, error)

	KeyboardInteractiveCallback func(conn ConnMetadata, client KeyboardInteractiveChallenge) (*Upstream, error)

	UpstreamAuthFailureCallback func(conn ConnMetadata, method string, err error, allowmethods []string)

	BannerCallback func(conn ConnMetadata) string

	VerifyHostKeyCallback func(conn ConnMetadata, hostname, netaddr string, key []byte) error

	PipeCreateErrorCallback func(remoteAddr string, err error)

	PipeStartCallback func(conn ConnMetadata)

	PipeErrorCallback func(conn ConnMetadata, err error)

	GrpcRemoteSignerFactory grpcsigner.SignerFactory
}

type SshPiperPlugin interface {
	SetConfigLoggerCallback(cb func(w io.Writer, level string, tty bool))
	// GetGrpcServer() *grpc.Server
	Serve() error
}

func NewFromStdio(config SshPiperPluginConfig) (SshPiperPlugin, error) {
	s := grpc.NewServer()
	l, err := ioconn.ListenFromSingleIO(os.Stdin, os.Stdout)
	if err != nil {
		return nil, err
	}

	return NewFromGrpc(config, s, l)
}

func NewFromGrpc(config SshPiperPluginConfig, grpc *grpc.Server, listener net.Listener) (SshPiperPlugin, error) {
	r, w := io.Pipe()

	s := &server{
		config:    config,
		grpc:      grpc,
		listener:  listener,
		logwriter: w,
		logs:      make(chan string, 1000),
	}

	go func() {
		scanner := bufio.NewScanner(r)

		for scanner.Scan() {
			s.logs <- scanner.Text()
		}
	}()

	RegisterSshPiperPluginServer(s.grpc, s)

	if config.GrpcRemoteSignerFactory != nil {
		gs, err := grpcsigner.NewSignerServer(config.GrpcRemoteSignerFactory)
		if err != nil {
			return nil, err
		}
		grpcsigner.RegisterSignerServer(s.grpc, gs)
	}

	return s, nil
}

type server struct {
	UnimplementedSshPiperPluginServer

	config   SshPiperPluginConfig
	grpc     *grpc.Server
	listener net.Listener

	logconfigcb func(w io.Writer, level string, tty bool)
	logs        chan string
	logwriter   io.Writer
}

func (s *server) GetGrpcServer() *grpc.Server {
	return s.grpc
}

func (s *server) GetLoggerOutput() io.Writer {
	return s.logwriter
}

func (s *server) SetConfigLoggerCallback(cb func(w io.Writer, level string, tty bool)) {
	s.logconfigcb = cb
}

func (s *server) Serve() error {
	return s.grpc.Serve(s.listener)
}

func (s *server) Logs(req *StartLogRequest, stream SshPiperPlugin_LogsServer) error {
	if s.logconfigcb != nil {
		s.logconfigcb(s.logwriter, req.Level, req.Tty)
	}

	for log := range s.logs {
		if err := stream.Send(&Log{
			Message: log,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (s *server) ListCallbacks(ctx context.Context, req *ListCallbackRequest) (*ListCallbackResponse, error) {
	var cb []string

	if s.config.NewConnectionCallback != nil {
		cb = append(cb, "NewConnection")
	}

	if s.config.NextAuthMethodsCallback != nil {
		cb = append(cb, "NextAuthMethods")
	}

	if s.config.NoClientAuthCallback != nil {
		cb = append(cb, "NoneAuth")
	}

	if s.config.PasswordCallback != nil {
		cb = append(cb, "PasswordAuth")
	}

	if s.config.PublicKeyCallback != nil {
		cb = append(cb, "PublicKeyAuth")
	}

	if s.config.KeyboardInteractiveCallback != nil {
		cb = append(cb, "KeyboardInteractiveAuth")
	}

	if s.config.UpstreamAuthFailureCallback != nil {
		cb = append(cb, "UpstreamAuthFailure")
	}

	if s.config.BannerCallback != nil {
		cb = append(cb, "Banner")
	}

	if s.config.VerifyHostKeyCallback != nil {
		cb = append(cb, "VerifyHostKey")
	}

	if s.config.PipeStartCallback != nil {
		cb = append(cb, "PipeStart")
	}

	if s.config.PipeErrorCallback != nil {
		cb = append(cb, "PipeError")
	}

	if s.config.PipeCreateErrorCallback != nil {
		cb = append(cb, "PipeCreateError")
	}

	return &ListCallbackResponse{
		Callbacks: cb,
	}, nil
}

func (s *server) NewConnection(ctx context.Context, req *NewConnectionRequest) (*NewConnectionResponse, error) {
	if s.config.NewConnectionCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method NewConnection not implemented")
	}

	if err := s.config.NewConnectionCallback(req.Meta); err != nil {
		return nil, err
	}

	return &NewConnectionResponse{}, nil
}

func (s *server) NextAuthMethods(ctx context.Context, req *NextAuthMethodsRequest) (*NextAuthMethodsResponse, error) {
	if s.config.NextAuthMethodsCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method NextAuthMethods not implemented")
	}

	methods, err := s.config.NextAuthMethodsCallback(req.Meta)
	if err != nil {
		return nil, err
	}

	resp := &NextAuthMethodsResponse{}

	for _, method := range methods {
		m := AuthMethodFromName(method)
		if m == -1 {
			return nil, status.Errorf(codes.InvalidArgument, "unknown method %s", method)
		}
		resp.Methods = append(resp.Methods, m)
	}

	return resp, nil
}

func (s *server) NoneAuth(ctx context.Context, req *NoneAuthRequest) (*NoneAuthResponse, error) {
	if s.config.NoClientAuthCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method NoneAuth not implemented")
	}

	upstream, err := s.config.NoClientAuthCallback(req.Meta)
	if err != nil {
		if denial, ok := asDenial(err); ok {
			return &NoneAuthResponse{Result: &NoneAuthResponse_Denial{Denial: denial}}, nil
		}
		return nil, err
	}

	return &NoneAuthResponse{Result: &NoneAuthResponse_Upstream{Upstream: upstream}}, nil
}

func (s *server) PasswordAuth(ctx context.Context, req *PasswordAuthRequest) (*PasswordAuthResponse, error) {
	if s.config.PasswordCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method PasswordAuth not implemented")
	}

	upstream, err := s.config.PasswordCallback(req.Meta, req.Password)
	if err != nil {
		if denial, ok := asDenial(err); ok {
			return &PasswordAuthResponse{Result: &PasswordAuthResponse_Denial{Denial: denial}}, nil
		}
		return nil, err
	}

	return &PasswordAuthResponse{Result: &PasswordAuthResponse_Upstream{Upstream: upstream}}, nil
}

func (s *server) PublicKeyAuth(ctx context.Context, req *PublicKeyAuthRequest) (*PublicKeyAuthResponse, error) {
	if s.config.PublicKeyCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method PublicKeyAuth not implemented")
	}

	upstream, err := s.config.PublicKeyCallback(req.Meta, req.PublicKey)
	if err != nil {
		if denial, ok := asDenial(err); ok {
			return &PublicKeyAuthResponse{Result: &PublicKeyAuthResponse_Denial{Denial: denial}}, nil
		}
		return nil, err
	}

	return &PublicKeyAuthResponse{Result: &PublicKeyAuthResponse_Upstream{Upstream: upstream}}, nil
}

func (s *server) KeyboardInteractiveAuth(stream SshPiperPlugin_KeyboardInteractiveAuthServer) error {
	if s.config.KeyboardInteractiveCallback == nil {
		return status.Errorf(codes.Unimplemented, "method KeyboardInteractiveAuth not implemented")
	}

	if err := stream.Send(&KeyboardInteractiveAuthMessage{
		Message: &KeyboardInteractiveAuthMessage_MetaRequest{},
	}); err != nil {
		return err
	}

	metareply, err := stream.Recv()
	if err != nil {
		return err
	}

	meta := metareply.GetMetaResponse()
	if meta == nil {
		return status.Errorf(codes.InvalidArgument, "missing meta")
	}

	upstream, err := s.config.KeyboardInteractiveCallback(meta.Meta, func(user, instruction string, question string, echo bool) (answer string, err error) {
		var questions []*KeyboardInteractivePromptRequest_Question
		if question != "" {
			questions = append(questions, &KeyboardInteractivePromptRequest_Question{
				Text: question,
				Echo: echo,
			})
		}

		if err := stream.Send(&KeyboardInteractiveAuthMessage{
			Message: &KeyboardInteractiveAuthMessage_PromptRequest{
				PromptRequest: &KeyboardInteractivePromptRequest{
					Name:        user,
					Instruction: instruction,
					Questions:   questions,
				},
			},
		}); err != nil {
			return "", err
		}

		if question == "" {
			return "", nil
		}

		userInputReply, err := stream.Recv()
		if err != nil {
			return "", err
		}

		userInput := userInputReply.GetUserResponse()
		if userInput == nil {
			return "", status.Errorf(codes.InvalidArgument, "missing user input")
		}

		if len(userInput.Answers) != 1 {
			return "", status.Errorf(codes.InvalidArgument, "expected 1 answer, got %d", len(userInput.Answers))
		}

		return userInput.Answers[0], nil
	})
	finish := &KeyboardInteractiveFinishRequest{}
	if err != nil {
		denial, ok := asDenial(err)
		if !ok {
			return err
		}
		finish.Result = &KeyboardInteractiveFinishRequest_Denial{Denial: denial}
	} else {
		finish.Result = &KeyboardInteractiveFinishRequest_Upstream{Upstream: upstream}
	}

	if err := stream.Send(&KeyboardInteractiveAuthMessage{
		Message: &KeyboardInteractiveAuthMessage_FinishRequest{
			FinishRequest: finish,
		},
	}); err != nil {
		return err
	}

	return nil
}

func (s *server) UpstreamAuthFailureNotice(ctx context.Context, req *UpstreamAuthFailureNoticeRequest) (*UpstreamAuthFailureNoticeResponse, error) {
	if s.config.UpstreamAuthFailureCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method UpstreamAuthFailureNotice not implemented")
	}

	var methods []string

	for _, method := range req.GetAllowedMethods() {
		m := AuthMethodTypeToName(method)
		if m == "" {
			continue
		}
		methods = append(methods, m)
	}

	s.config.UpstreamAuthFailureCallback(req.Meta, req.Method, fmt.Errorf("%v", req.Error), methods)

	return &UpstreamAuthFailureNoticeResponse{}, nil
}

func (s *server) Banner(ctx context.Context, req *BannerRequest) (*BannerResponse, error) {
	if s.config.BannerCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method Banner not implemented")
	}

	msg := s.config.BannerCallback(req.Meta)

	return &BannerResponse{
		Message: msg,
	}, nil
}

func (s *server) VerifyHostKey(ctx context.Context, req *VerifyHostKeyRequest) (*VerifyHostKeyResponse, error) {
	if s.config.VerifyHostKeyCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method VerifyHostKey not implemented")
	}

	err := s.config.VerifyHostKeyCallback(req.Meta, req.Hostname, req.Netaddress, req.Key)
	if err != nil {
		return nil, err
	}

	return &VerifyHostKeyResponse{
		Verified: true,
	}, nil
}

func (s *server) PipeStartNotice(ctx context.Context, req *PipeStartNoticeRequest) (*PipeStartNoticeResponse, error) {
	if s.config.PipeStartCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method PipeStartNotice not implemented")
	}

	s.config.PipeStartCallback(req.Meta)

	return &PipeStartNoticeResponse{}, nil
}

func (s *server) PipeErrorNotice(ctx context.Context, req *PipeErrorNoticeRequest) (*PipeErrorNoticeResponse, error) {
	if s.config.PipeErrorCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method PipeErrorNotice not implemented")
	}

	s.config.PipeErrorCallback(req.Meta, fmt.Errorf("%v", req.Error))

	return &PipeErrorNoticeResponse{}, nil
}

func (s *server) PipeCreateErrorNotice(ctx context.Context, req *PipeCreateErrorNoticeRequest) (*PipeCreateErrorNoticeResponse, error) {
	if s.config.PipeCreateErrorCallback == nil {
		return nil, status.Errorf(codes.Unimplemented, "method PipeCreateErrorNotice not implemented")
	}

	s.config.PipeCreateErrorCallback(req.FromAddr, fmt.Errorf("%v", req.Error))

	return &PipeCreateErrorNoticeResponse{}, nil
}
