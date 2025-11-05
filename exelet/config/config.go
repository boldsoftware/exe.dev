package config

import "time"

const (
	// DefaultServerAddress is the default exelet server address
	DefaultExeletAddress = "tcp://127.0.0.1:8080"

	// DefaultNameserver is the default instance nameserver
	DefaultNameserver = "1.1.1.1"

	// EnvVarExeletServerAddress is the environment variable to resolve the exelet address
	EnvVarExeletServerAddress = "EXELET_ADDR"

	// DefaultInstanceDomain is the default domain for all instances
	DefaultInstanceDomain = "exe.dev"
)

var (
	// InstanceStartTimeout is the timeout for which to wait for instance start
	InstanceStartTimeout = time.Second * 60
	// InstanceStopTimeout is the timeout for which to wait for instance stop
	InstanceStopTimeout = time.Second * 10
	// InstanceAgentConnectTimeout is the timeout to connect to an instance agent
	InstanceAgentConnectTimeout = time.Second * 10
	// InstanceSSHHostKeyPath is the path in the guest for the ssh host identity
	InstanceSSHHostKeyPath = "/exe.dev/etc/ssh/ssh_host_ed25519_key"
	// InstanceSSHKeyDir is the directory in the instance for ssh keys
	InstanceSSHPublicKeysPath = "/exe.dev/etc/ssh/authorized_keys"
	// HostnamePath is the path in the instance for the hostname
	HostnamePath = "/etc/hostname"
	// HostsPath is the path in the instance for local hosts resolution
	HostsPath = "/etc/hosts"
	// ResolvConfPath is the path in the instance for the resolv.conf
	ResolvConfPath = "/etc/resolv.conf"
	// ImageConfigPath is the path in the instance for the image configuration
	ImageConfigPath = "/exe.dev/etc/image.conf"
	// EnvConfigPath is the path in the instance for the environment configuration
	EnvConfigPath = "/exe.dev/etc/env"
	// InstanceAgentConfigPath is the path in the instance for the exe agent configuration
	InstanceAgentConfigPath = "/exe.dev/etc/agent.conf"
	// InstanceExeInitPath is the path in the instance for exe-init
	InstanceExeInitPath = "/exe.dev/bin/exe-init"
	// InstanceExeSshPath is the path in the instance for exe-ssh
	InstanceExeSshPath = "/exe.dev/bin/sshd"

	// InstanceVsockAgentPort is the vsock port on which the agent listens in the guest
	InstanceAgentPort = 1090
)

// ExeletConfig is the configuration used for the server
type ExeletConfig struct {
	// Name is the name of the node
	Name string
	// ListenAddress is the address for the server
	ListenAddress string
	// DataDir is the local data directory
	DataDir string
	// Region is the locality region
	Region string
	// Zone is the locality zone
	Zone string
	// RuntimeAddress is the address to the runtime
	RuntimeAddress string
	// NetworkManagerAddress is the address to the network manager
	NetworkManagerAddress string
	// StorageManagerAddress is the address for the storage manager
	StorageManagerAddress string
	// TLSCertificate is the certificate used for grpc communication
	TLSServerCertificate string
	// TLSKey is the key used for grpc communication
	TLSServerKey string
	// TLSClientCertificate is the client certificate used for communication
	TLSClientCertificate string
	// TLSClientKey is the client key used for communication
	TLSClientKey string
	// TLSInsecureSkipVerify disables certificate verification
	TLSInsecureSkipVerify bool

	// enableInstanceBootOnStartup enables booting compute instances on server start
	EnableInstanceBootOnStartup bool
	// AgentUseVsock configures the agent communication to use vsock
	AgentUseVsock bool
}
