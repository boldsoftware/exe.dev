package execonnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	controlSocketName      = "control.sock"
	maxControlRequestSize  = 64 << 10
	maxControlResponseSize = 1 << 20
	controlBindAttempts    = 3
	controlShutdownTimeout = 5 * time.Second
)

var (
	// ErrServeRunning indicates that another serve process owns the control socket.
	ErrServeRunning = errors.New("execonnect serve is already running for this socket")
	// ErrServeNotRunning indicates that a command requires the local daemon.
	ErrServeNotRunning = errors.New("execonnect is not running; start 'execonnect serve' in a foreground terminal (Linux/macOS) or start the execonnect systemd service (Linux)")
	// ErrEnrollmentConflict indicates that enrollment changed after a client preflight.
	ErrEnrollmentConflict = errors.New("enrollment changed concurrently")
	socketUmaskMu         sync.Mutex
)

type controlEndpointRequest struct {
	Name          string `json:"name"`
	URL           string `json:"url,omitempty"`
	TLSServerName string `json:"tls_server_name,omitempty"`
}

type controlEnrollRequest struct {
	ExpectedOldID string `json:"expected_old_id"`
	ExedURL       string `json:"exed_url"`
	Token         string `json:"token"`
}

type controlEnrollment struct {
	ExternalConnectionID string `json:"external_connection_id"`
}

type controlStatus struct {
	InstanceNonce        string `json:"instance_nonce"`
	Service              string `json:"service"`
	Enrollment           string `json:"enrollment"`
	ExternalConnectionID string `json:"external_connection_id,omitempty"`
	ExedURL              string `json:"exed_url,omitempty"`
	EndpointCount        int    `json:"endpoint_count"`
	Control              string `json:"control"`
	Tunnel               string `json:"tunnel"`
	Error                string `json:"error,omitempty"`
}

type controlError struct {
	Code                        string `json:"code"`
	Message                     string `json:"message"`
	CurrentExternalConnectionID string `json:"current_external_connection_id,omitempty"`
	GRPCCode                    int32  `json:"grpc_code,omitempty"`
}

type controlResponse struct {
	Changed    bool                     `json:"changed,omitempty"`
	Endpoints  []endpointRegistryRecord `json:"endpoints,omitempty"`
	Enrollment *controlEnrollment       `json:"enrollment,omitempty"`
	Status     *controlStatus           `json:"status,omitempty"`
	Error      *controlError            `json:"error,omitempty"`
}

type enrollmentConflictError struct {
	currentID string
	message   string
}

func (err *enrollmentConflictError) Error() string { return err.message }
func (err *enrollmentConflictError) Unwrap() error { return ErrEnrollmentConflict }

type localControlServer struct {
	daemon *daemon
}

func newLocalControlServer(daemon *daemon) *localControlServer {
	return &localControlServer{daemon: daemon}
}

func (server *localControlServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/enroll":
		server.handleEnroll(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/endpoints/add":
		server.handleMutation(writer, request, "add")
	case request.Method == http.MethodPost && request.URL.Path == "/endpoints/update":
		server.handleMutation(writer, request, "update")
	case request.Method == http.MethodPost && request.URL.Path == "/endpoints/remove":
		server.handleMutation(writer, request, "remove")
	case request.Method == http.MethodGet && request.URL.Path == "/endpoints":
		server.handleList(writer)
	case request.Method == http.MethodGet && request.URL.Path == "/status":
		server.handleStatus(writer)
	default:
		writeControlError(writer, http.StatusNotFound, "not_found", "unknown control operation")
	}
}

func (server *localControlServer) handleEnroll(writer http.ResponseWriter, request *http.Request) {
	var input controlEnrollRequest
	if err := decodeBoundedJSON(request.Body, maxControlRequestSize, &input); err != nil {
		writeControlError(writer, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode request: %v", err))
		return
	}
	state, err := server.daemon.enroll(request.Context(), input)
	if err != nil {
		var conflict *enrollmentConflictError
		if errors.As(err, &conflict) {
			writeControlJSON(writer, http.StatusConflict, controlResponse{Error: &controlError{
				Code:                        "enrollment_conflict",
				Message:                     conflict.Error(),
				CurrentExternalConnectionID: conflict.currentID,
			}})
			return
		}
		grpcCode := status.Code(err)
		if grpcCode != codes.Unknown {
			writeControlJSON(writer, http.StatusBadGateway, controlResponse{Error: &controlError{
				Code:     "remote_enrollment",
				Message:  status.Convert(err).Message(),
				GRPCCode: int32(grpcCode),
			}})
			return
		}
		writeControlError(writer, http.StatusInternalServerError, "enrollment_failed", err.Error())
		return
	}
	writeControlJSON(writer, http.StatusOK, controlResponse{Enrollment: &controlEnrollment{
		ExternalConnectionID: state.ExternalConnectionID,
	}})
}

func (server *localControlServer) handleMutation(writer http.ResponseWriter, request *http.Request, operation string) {
	var input controlEndpointRequest
	if err := decodeBoundedJSON(request.Body, maxControlRequestSize, &input); err != nil {
		writeControlError(writer, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode request: %v", err))
		return
	}
	snapshot, changed, err := server.daemon.mutateEndpoint(operation, input)
	if err != nil {
		statusCode := http.StatusInternalServerError
		code := "internal"
		var mutationErr *endpointMutationError
		if errors.As(err, &mutationErr) {
			code = mutationErr.kind
			if mutationErr.kind == "endpoint_invalid" {
				statusCode = http.StatusBadRequest
			} else if mutationErr.kind == "daemon_starting" {
				statusCode = http.StatusServiceUnavailable
			}
		}
		records, recordErr := recordsFromEndpoints(snapshot.Endpoints)
		if recordErr != nil {
			writeControlError(writer, http.StatusInternalServerError, "internal", recordErr.Error())
			return
		}
		writeControlJSON(writer, statusCode, controlResponse{
			Changed:   changed,
			Endpoints: records,
			Error:     &controlError{Code: code, Message: err.Error()},
		})
		return
	}
	writeEndpointSnapshot(writer, snapshot, changed)
}

func (server *localControlServer) handleList(writer http.ResponseWriter) {
	snapshot, err := server.daemon.listEndpoints()
	if err != nil {
		writeControlError(writer, http.StatusServiceUnavailable, "daemon_starting", err.Error())
		return
	}
	writeEndpointSnapshot(writer, snapshot, false)
}

func (server *localControlServer) handleStatus(writer http.ResponseWriter) {
	writeControlJSON(writer, http.StatusOK, controlResponse{Status: server.daemon.status()})
}

func writeEndpointSnapshot(writer http.ResponseWriter, snapshot EndpointRegistrySnapshot, changed bool) {
	records, err := recordsFromEndpoints(snapshot.Endpoints)
	if err != nil {
		writeControlError(writer, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeControlJSON(writer, http.StatusOK, controlResponse{Changed: changed, Endpoints: records})
}

func writeControlError(writer http.ResponseWriter, statusCode int, code, message string) {
	writeControlJSON(writer, statusCode, controlResponse{Error: &controlError{Code: code, Message: message}})
}

func writeControlJSON(writer http.ResponseWriter, statusCode int, response controlResponse) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(response)
}

func decodeBoundedJSON(reader io.Reader, limit int64, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("JSON body is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("JSON body must contain one document")
	} else if err != io.EOF {
		return err
	}
	return nil
}

type controlSocket struct {
	listener *net.UnixListener
	path     string
	info     os.FileInfo
}

func listenControlSocket(path string) (*controlSocket, error) {
	if err := ensureRuntimeDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < controlBindAttempts; attempt++ {
		socket, err := bindControlSocket(path)
		if err == nil {
			return socket, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("listen on control socket: %w", err)
		}
		stale, err := staleControlSocket(path)
		if err != nil {
			return nil, err
		}
		if !stale {
			return nil, ErrServeRunning
		}
	}
	return nil, fmt.Errorf("claim control socket after %d attempts: %w", controlBindAttempts, ErrServeRunning)
}

func bindControlSocket(path string) (*controlSocket, error) {
	socketUmaskMu.Lock()
	oldUmask := syscall.Umask(0o117)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(oldUmask)
	socketUmaskMu.Unlock()
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o660); err != nil {
		listener.Close()
		return nil, fmt.Errorf("set control socket permissions: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("stat control socket: %w", err)
	}
	return &controlSocket{listener: listener, path: path, info: info}, nil
}

func staleControlSocket(path string) (bool, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect occupied control socket: %w", err)
	}
	if before.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("control path %q is not a Unix socket", path)
	}
	connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		connection.Close()
		return false, nil
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return false, fmt.Errorf("probe occupied control socket: %w", dialErr)
	}
	after, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify stale control socket: %w", err)
	}
	if after.Mode()&os.ModeSocket == 0 || !os.SameFile(before, after) {
		return true, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove stale control socket: %w", err)
	}
	return true, nil
}

func ensureRuntimeDirectory(path string) error {
	if err := os.MkdirAll(path, 0o770); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime path %q is not a directory", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect runtime directory ownership")
	}
	if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
		return fmt.Errorf("runtime directory %q must be owned by uid %d and gid %d", path, os.Geteuid(), os.Getegid())
	}
	if err := os.Chmod(path, 0o770); err != nil {
		return fmt.Errorf("set runtime directory permissions: %w", err)
	}
	return nil
}

func (socket *controlSocket) close(remove bool) error {
	if socket == nil {
		return nil
	}
	var closeErr error
	if socket.listener != nil {
		closeErr = socket.listener.Close()
		if errors.Is(closeErr, net.ErrClosed) {
			closeErr = nil
		}
		socket.listener = nil
	}
	if !remove {
		return closeErr
	}
	info, err := os.Lstat(socket.path)
	if errors.Is(err, os.ErrNotExist) {
		return closeErr
	}
	if err != nil {
		return errors.Join(closeErr, fmt.Errorf("inspect control socket during cleanup: %w", err))
	}
	if info.Mode()&os.ModeSocket == 0 || !os.SameFile(socket.info, info) {
		return closeErr
	}
	if err := os.Remove(socket.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(closeErr, fmt.Errorf("remove control socket: %w", err))
	}
	return closeErr
}

func (socket *controlSocket) Close() error { return socket.close(true) }

func runControlServer(ctx context.Context, listener net.Listener, handler http.Handler) error {
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caf := context.AfterFunc(ctx, cancel)
	defer caf()

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return sctx },
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve local control socket: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), controlShutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, server.Close())
		}
		serveErr := <-result
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func callControl(ctx context.Context, socketPath, method, path string, requestValue any) (controlResponse, error) {
	var body io.Reader
	if requestValue != nil {
		data, err := json.Marshal(requestValue)
		if err != nil {
			return controlResponse{}, fmt.Errorf("encode control request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return controlResponse{}, fmt.Errorf("create control request: %w", err)
	}
	if requestValue != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	httpResponse, err := client.Do(request)
	if err != nil {
		return controlResponse{}, err
	}
	defer httpResponse.Body.Close()
	var response controlResponse
	if err := decodeBoundedJSON(httpResponse.Body, maxControlResponseSize, &response); err != nil {
		return controlResponse{}, fmt.Errorf("decode control response: %w", err)
	}
	if response.Error != nil {
		switch response.Error.Code {
		case "enrollment_conflict":
			return controlResponse{}, &enrollmentConflictError{
				currentID: response.Error.CurrentExternalConnectionID,
				message:   response.Error.Message,
			}
		case "remote_enrollment":
			return controlResponse{}, status.Error(codes.Code(response.Error.GRPCCode), response.Error.Message)
		default:
			return controlResponse{}, errors.New(response.Error.Message)
		}
	}
	if httpResponse.StatusCode != http.StatusOK {
		return controlResponse{}, fmt.Errorf("control request failed with HTTP %d", httpResponse.StatusCode)
	}
	return response, nil
}

func controlUnavailable(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}
