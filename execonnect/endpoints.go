package execonnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"
)

const (
	// MaxEndpoints is the maximum number of active endpoints in one connector.
	MaxEndpoints          = 100
	maxEndpointURLLength  = 2048
	maxEndpointHostLength = 253
)

var endpointKeyPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Endpoint maps a published endpoint key to a local target URL.
type Endpoint struct {
	// Key is the immutable durable endpoint name.
	Key string
	// URL is the canonical local target URL.
	URL string
	// DisplayName labels the endpoint when a user selects it for an integration.
	DisplayName string
	// Target is the normalized local TCP host and port to dial.
	Target string
	// Protocol describes how integrations use the endpoint.
	Protocol connectapi.EndpointProtocol
	// TLSServerName is the effective TLS server name for HTTPS endpoints.
	TLSServerName string
}

type endpointTarget struct {
	address string
	host    string
	port    int
}

type endpointRoute struct {
	target        endpointTarget
	protocol      connectapi.EndpointProtocol
	tlsServerName string
}

type endpointSnapshot struct {
	targets map[string]endpointTarget
	routes  map[string]endpointRoute
}

var errEndpointFlowTerminated = errors.New("endpoint flow terminated")

type endpointNetworkDialer func(context.Context, string, string) (net.Conn, error)

// EndpointRegistry stores endpoint routes and the flows currently using them.
type EndpointRegistry struct {
	snapshot atomic.Pointer[endpointSnapshot]

	mu          sync.Mutex
	active      map[string]map[*endpointFlow]struct{}
	dialContext endpointNetworkDialer
}

type endpointFlow struct {
	registry *EndpointRegistry
	endpoint string
	cancel   context.CancelFunc
	done     chan struct{}

	mu          sync.Mutex
	connection  net.Conn
	closing     bool
	terminating bool
	released    bool
	closeErr    error
}

type trackedEndpointConnection struct {
	net.Conn
	flow *endpointFlow
}

// NewEndpointRegistry creates a registry containing endpoints.
func NewEndpointRegistry(endpoints []Endpoint) (*EndpointRegistry, error) {
	dialer := &net.Dialer{}
	registry := &EndpointRegistry{
		active:      make(map[string]map[*endpointFlow]struct{}),
		dialContext: dialer.DialContext,
	}
	if err := registry.Replace(endpoints); err != nil {
		return nil, err
	}
	return registry, nil
}

// Replace validates and atomically replaces all registered endpoints, terminating flows whose routes disappeared or changed.
func (r *EndpointRegistry) Replace(endpoints []Endpoint) error {
	snapshot, err := buildEndpointTargets(endpoints)
	if err != nil {
		return err
	}
	return r.replace(snapshot)
}

func (r *EndpointRegistry) replace(snapshot *endpointSnapshot) error {
	// DialContext registers flows under the same lock, so a flow either belongs
	// to the previous route and is collected here or observes snapshot.
	r.mu.Lock()
	previous := r.snapshot.Load()
	r.snapshot.Store(snapshot)
	flows := r.flowsForChangedRoutesLocked(previous, snapshot)
	r.mu.Unlock()

	connections := make(map[*endpointFlow]net.Conn, len(flows))
	for _, flow := range flows {
		if connection := flow.beginTermination(); connection != nil {
			connections[flow] = connection
		}
	}
	for flow, connection := range connections {
		flow.finish(connection.Close())
	}
	var closeErrors []error
	for _, flow := range flows {
		if err := flow.wait(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close endpoint %q flow: %w", flow.endpoint, err))
		}
	}
	return errors.Join(closeErrors...)
}

func (r *EndpointRegistry) flowsForChangedRoutesLocked(previous, next *endpointSnapshot) []*endpointFlow {
	if previous == nil {
		return nil
	}
	var flows []*endpointFlow
	for endpoint, previousRoute := range previous.routes {
		nextRoute, ok := next.routes[endpoint]
		if ok && nextRoute == previousRoute {
			continue
		}
		for flow := range r.active[endpoint] {
			flows = append(flows, flow)
		}
	}
	return flows
}

func buildEndpointTargets(endpoints []Endpoint) (*endpointSnapshot, error) {
	if len(endpoints) > MaxEndpoints {
		return nil, fmt.Errorf("endpoint limit is %d", MaxEndpoints)
	}
	targets := make(map[string]endpointTarget, len(endpoints))
	routes := make(map[string]endpointRoute, len(endpoints))
	for _, endpoint := range endpoints {
		normalized, target, err := normalizeEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		if _, ok := targets[normalized.Key]; ok {
			return nil, fmt.Errorf("duplicate endpoint name %q", normalized.Key)
		}
		targets[normalized.Key] = target
		routes[normalized.Key] = endpointRoute{
			target:        target,
			protocol:      normalized.Protocol,
			tlsServerName: normalized.TLSServerName,
		}
	}
	return &endpointSnapshot{targets: targets, routes: routes}, nil
}

// NormalizeEndpoint validates and canonicalizes one CLI endpoint definition.
func NormalizeEndpoint(name, rawURL, tlsServerName string) (Endpoint, error) {
	name = strings.TrimSpace(name)
	if !endpointKeyPattern.MatchString(name) {
		return Endpoint{}, fmt.Errorf("invalid endpoint name %q: use a 1-63 character lowercase DNS label", name)
	}
	if rawURL == "" || rawURL != strings.TrimSpace(rawURL) {
		return Endpoint{}, fmt.Errorf("endpoint %q has invalid URL %q", name, rawURL)
	}
	if len(rawURL) > maxEndpointURLLength {
		return Endpoint{}, fmt.Errorf("endpoint %q URL is too long", name)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Endpoint{}, fmt.Errorf("parse endpoint %q URL: %w", name, err)
	}
	if parsed.Opaque != "" {
		return Endpoint{}, fmt.Errorf("endpoint %q URL must use // before the host", name)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Scheme != scheme {
		return Endpoint{}, fmt.Errorf("endpoint %q URL scheme must be lowercase", name)
	}
	var protocol connectapi.EndpointProtocol
	var defaultPort int
	switch scheme {
	case "http":
		protocol = connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTP
		defaultPort = 80
	case "https":
		protocol = connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTPS
		defaultPort = 443
	case "tcp":
		protocol = connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_TCP
	default:
		return Endpoint{}, fmt.Errorf("endpoint %q URL scheme must be http, https, or tcp", name)
	}
	if parsed.User != nil {
		return Endpoint{}, fmt.Errorf("endpoint %q URL must not include user information", name)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Endpoint{}, fmt.Errorf("endpoint %q URL must not include a path", name)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return Endpoint{}, fmt.Errorf("endpoint %q URL must not include a query", name)
	}
	if parsed.Fragment != "" || strings.Contains(rawURL, "#") {
		return Endpoint{}, fmt.Errorf("endpoint %q URL must not include a fragment", name)
	}

	host, err := normalizeEndpointHost(parsed.Hostname())
	if err != nil {
		return Endpoint{}, fmt.Errorf("endpoint %q URL has invalid host: %w", name, err)
	}
	portText, explicitPort, err := endpointURLPort(parsed.Host)
	if err != nil {
		return Endpoint{}, fmt.Errorf("endpoint %q URL has invalid port: %w", name, err)
	}
	port := defaultPort
	if explicitPort {
		parsedPort, err := strconv.ParseUint(portText, 10, 16)
		if err != nil || parsedPort == 0 {
			return Endpoint{}, fmt.Errorf("endpoint %q URL has invalid port %q", name, portText)
		}
		port = int(parsedPort)
	} else if scheme == "tcp" {
		return Endpoint{}, fmt.Errorf("endpoint %q tcp URL requires an explicit port", name)
	}

	tlsServerName = strings.TrimSpace(tlsServerName)
	if protocol != connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTPS && tlsServerName != "" {
		return Endpoint{}, fmt.Errorf("--tls-server-name is only valid for https endpoints")
	}
	if protocol == connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTPS {
		if tlsServerName == "" {
			tlsServerName = host
		} else {
			tlsServerName, err = normalizeEndpointHost(tlsServerName)
			if err != nil {
				return Endpoint{}, fmt.Errorf("endpoint %q has invalid TLS server name: %w", name, err)
			}
		}
	}

	urlHost := host
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && address.Is6() {
		urlHost = "[" + host + "]"
	}
	includePort := scheme == "tcp" || port != defaultPort
	canonicalURL := scheme + "://" + urlHost
	if includePort {
		canonicalURL += ":" + strconv.Itoa(port)
	}
	return Endpoint{
		Key:           name,
		URL:           canonicalURL,
		DisplayName:   name,
		Target:        net.JoinHostPort(host, strconv.Itoa(port)),
		Protocol:      protocol,
		TLSServerName: tlsServerName,
	}, nil
}

func normalizeEndpoint(endpoint Endpoint) (Endpoint, endpointTarget, error) {
	if endpoint.URL != "" {
		normalized, err := NormalizeEndpoint(endpoint.Key, endpoint.URL, endpoint.TLSServerName)
		if err != nil {
			return Endpoint{}, endpointTarget{}, err
		}
		return normalized, endpointTargetFromEndpoint(normalized), nil
	}

	// Target-only endpoints are retained for local callers that already have
	// validated wire metadata. The CLI and persistent registry only use URLs.
	key := strings.TrimSpace(endpoint.Key)
	if !endpointKeyPattern.MatchString(key) {
		return Endpoint{}, endpointTarget{}, fmt.Errorf("invalid endpoint name %q", endpoint.Key)
	}
	host, portText, err := net.SplitHostPort(strings.TrimSpace(endpoint.Target))
	if err != nil || host == "" {
		return Endpoint{}, endpointTarget{}, fmt.Errorf("endpoint %q target must be host:port", key)
	}
	host, err = normalizeEndpointHost(host)
	if err != nil {
		return Endpoint{}, endpointTarget{}, fmt.Errorf("endpoint %q target has invalid host: %w", key, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return Endpoint{}, endpointTarget{}, fmt.Errorf("endpoint %q target has invalid port", key)
	}
	protocol := endpoint.Protocol
	if protocol == connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_UNSPECIFIED {
		protocol = connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_TCP
	}
	if protocol != connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_TCP || strings.TrimSpace(endpoint.TLSServerName) != "" {
		return Endpoint{}, endpointTarget{}, fmt.Errorf("endpoint %q target-only definition must use plain tcp", key)
	}
	address := net.JoinHostPort(host, strconv.Itoa(int(port)))
	return Endpoint{
		Key:         key,
		DisplayName: key,
		Target:      address,
		Protocol:    protocol,
	}, endpointTarget{address: address, host: host, port: int(port)}, nil
}

func endpointTargetFromEndpoint(endpoint Endpoint) endpointTarget {
	host, portText, _ := net.SplitHostPort(endpoint.Target)
	port, _ := strconv.Atoi(portText)
	return endpointTarget{address: endpoint.Target, host: host, port: port}
}

func normalizeEndpointHost(host string) (string, error) {
	if host == "" || host != strings.TrimSpace(host) {
		return "", fmt.Errorf("host is required")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String(), nil
	}
	looksLikeIPv4 := strings.Contains(host, ".")
	for _, character := range host {
		if character > 127 {
			return "", fmt.Errorf("host must contain only ASCII characters")
		}
		if character != '.' && (character < '0' || character > '9') {
			looksLikeIPv4 = false
		}
	}
	if looksLikeIPv4 {
		return "", fmt.Errorf("invalid IP address")
	}
	host = strings.ToLower(host)
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
		if host == "" {
			return "", fmt.Errorf("host is required")
		}
	}
	if len(host) > maxEndpointHostLength {
		return "", fmt.Errorf("host is too long")
	}
	for _, label := range strings.Split(host, ".") {
		if !endpointKeyPattern.MatchString(label) {
			return "", fmt.Errorf("invalid DNS label %q", label)
		}
	}
	return host, nil
}

func endpointURLPort(authority string) (string, bool, error) {
	if authority == "" {
		return "", false, fmt.Errorf("host is required")
	}
	if strings.HasPrefix(authority, "[") {
		closing := strings.LastIndex(authority, "]")
		if closing < 0 {
			return "", false, fmt.Errorf("missing closing bracket")
		}
		remainder := authority[closing+1:]
		switch {
		case remainder == "":
			return "", false, nil
		case strings.HasPrefix(remainder, ":") && len(remainder) > 1:
			return remainder[1:], true, nil
		default:
			return "", false, fmt.Errorf("malformed authority")
		}
	}
	switch strings.Count(authority, ":") {
	case 0:
		return "", false, nil
	case 1:
		_, port, ok := strings.Cut(authority, ":")
		if !ok || port == "" {
			return "", false, fmt.Errorf("port is empty")
		}
		return port, true, nil
	default:
		return "", false, fmt.Errorf("IPv6 addresses must use brackets")
	}
}

// BuildEndpointSnapshot validates endpoints and builds the metadata published
// to exed.
func BuildEndpointSnapshot(endpoints []Endpoint) (*connectapi.EndpointSnapshot, error) {
	if len(endpoints) > MaxEndpoints {
		return nil, fmt.Errorf("endpoint limit is %d", MaxEndpoints)
	}
	byKey := make(map[string]Endpoint, len(endpoints))
	targets := make(map[string]endpointTarget, len(endpoints))
	for _, endpoint := range endpoints {
		normalized, target, err := normalizeEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		if _, ok := byKey[normalized.Key]; ok {
			return nil, fmt.Errorf("duplicate endpoint name %q", normalized.Key)
		}
		byKey[normalized.Key] = normalized
		targets[normalized.Key] = target
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	snapshot := &connectapi.EndpointSnapshot{Endpoints: make([]*connectapi.PublishedEndpoint, 0, len(keys))}
	for _, key := range keys {
		endpoint := byKey[key]
		target := targets[key]
		snapshot.Endpoints = append(snapshot.Endpoints, &connectapi.PublishedEndpoint{
			Key:           endpoint.Key,
			DisplayName:   endpoint.Key,
			TargetHost:    target.host,
			TargetPort:    uint32(target.port),
			Protocol:      endpoint.Protocol,
			TlsServerName: endpoint.TLSServerName,
		})
	}
	return snapshot, nil
}

// DialContext dials a registered endpoint when its published target port matches.
func (r *EndpointRegistry) DialContext(ctx context.Context, _ FlowContext, endpoint string, targetPort int) (net.Conn, error) {
	endpoint = strings.TrimSpace(endpoint)
	r.mu.Lock()
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown endpoint %q", endpoint)
	}
	target, ok := snapshot.targets[endpoint]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown endpoint %q", endpoint)
	}
	if targetPort != target.port {
		r.mu.Unlock()
		return nil, fmt.Errorf("endpoint %q target port %d does not match configured port %d", endpoint, targetPort, target.port)
	}
	// Register before dialing so route replacement can cancel a pending dial and
	// prevent a connection from attaching after its route changed.
	dialContext, cancel := context.WithCancel(ctx)
	flow := &endpointFlow{
		registry: r,
		endpoint: endpoint,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	if r.active == nil {
		r.active = make(map[string]map[*endpointFlow]struct{})
	}
	if r.active[endpoint] == nil {
		r.active[endpoint] = make(map[*endpointFlow]struct{})
	}
	r.active[endpoint][flow] = struct{}{}
	dial := r.dialContext
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	r.mu.Unlock()

	connection, err := dial(dialContext, "tcp", target.address)
	if err != nil {
		terminated := flow.finish(nil)
		if terminated {
			return nil, fmt.Errorf("%w: endpoint %q", errEndpointFlowTerminated, endpoint)
		}
		return nil, err
	}
	if !flow.attach(connection) {
		closeErr := connection.Close()
		flow.finish(closeErr)
		return nil, fmt.Errorf("%w: endpoint %q", errEndpointFlowTerminated, endpoint)
	}
	return &trackedEndpointConnection{Conn: connection, flow: flow}, nil
}

func (flow *endpointFlow) attach(connection net.Conn) bool {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.terminating || flow.released {
		return false
	}
	flow.connection = connection
	return true
}

func (flow *endpointFlow) beginTermination() net.Conn {
	flow.mu.Lock()
	if flow.released || flow.terminating {
		flow.mu.Unlock()
		return nil
	}
	flow.terminating = true
	cancel := flow.cancel
	connection := flow.connection
	if connection != nil && !flow.closing {
		flow.closing = true
	} else {
		connection = nil
	}
	flow.mu.Unlock()

	cancel()
	return connection
}

func (flow *endpointFlow) close() error {
	flow.mu.Lock()
	if flow.released {
		err := flow.closeErr
		flow.mu.Unlock()
		return err
	}
	if flow.closing {
		done := flow.done
		flow.mu.Unlock()
		<-done
		flow.mu.Lock()
		err := flow.closeErr
		flow.mu.Unlock()
		return err
	}
	flow.closing = true
	connection := flow.connection
	flow.mu.Unlock()

	err := connection.Close()
	flow.finish(err)
	return err
}

func (flow *endpointFlow) finish(closeErr error) bool {
	flow.mu.Lock()
	if flow.released {
		terminated := flow.terminating
		flow.mu.Unlock()
		return terminated
	}
	flow.released = true
	flow.closeErr = closeErr
	terminated := flow.terminating
	flow.mu.Unlock()

	flow.cancel()
	flow.registry.mu.Lock()
	delete(flow.registry.active[flow.endpoint], flow)
	if len(flow.registry.active[flow.endpoint]) == 0 {
		delete(flow.registry.active, flow.endpoint)
	}
	flow.registry.mu.Unlock()
	close(flow.done)
	return terminated
}

func (flow *endpointFlow) wait() error {
	<-flow.done
	flow.mu.Lock()
	defer flow.mu.Unlock()
	return flow.closeErr
}

func (connection *trackedEndpointConnection) Close() error {
	return connection.flow.close()
}

func (connection *trackedEndpointConnection) CloseWrite() error {
	if closeWriter, ok := connection.Conn.(interface{ CloseWrite() error }); ok {
		return closeWriter.CloseWrite()
	}
	return connection.Close()
}
