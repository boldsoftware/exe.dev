package config

import "time"

const (
	// DefaultServerAddress is the default exelet server address
	DefaultExeletAddress = "tcp://127.0.0.1:9080"

	// DefaultHTTPAddress is the default HTTP server address for debug and metrics
	DefaultHTTPAddress = ":9081"
	// DefaultResourceMonitorInterval is the default polling interval for the resource monitor
	DefaultResourceMonitorInterval = time.Minute
	// DefaultResourceManagerInterval is the default polling interval for the resource manager
	DefaultResourceManagerInterval = 30 * time.Second
	// DefaultIdleThreshold is the default duration after which a VM is considered idle
	DefaultIdleThreshold = 5 * time.Minute

	// DefaultNameserver is the default instance nameserver
	DefaultNameserver = "1.1.1.1"

	// EnvVarExeletServerAddress is the environment variable to resolve the exelet address
	EnvVarExeletServerAddress = "EXELET_ADDR"

	// DefaultInstanceDomain is the default domain for all instances
	DefaultInstanceDomain = "exe.xyz"
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
	// PasswdPath is the path in the instance for the users db
	PasswdPath = "/etc/passwd"
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
	// InstanceExeSshPrivilegeSeparationUser is the user in the instance that the ssh daemon uses for privilege separation
	InstanceExeSshPrivilegeSeparationUser = "sshd"
	// InstanceExeLabelUID is the label for the instance to specify the exe login user
	InstanceExeLabelLoginUser = "exe.dev/login-user"

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
	// IsProduction indicates that the exelet is running in a production environment
	IsProduction bool

	// enableInstanceBootOnStartup enables booting compute instances on server start
	EnableInstanceBootOnStartup bool
	// AgentUseVsock configures the agent communication to use vsock
	AgentUseVsock bool
	// ProxyPortMin is the minimum port for proxy allocation (defaults to 10000)
	ProxyPortMin int
	// ProxyPortMax is the maximum port for proxy allocation (defaults to 20000)
	ProxyPortMax int
	// ExedURL is the URL of the exed HTTP(S) server
	ExedURL string
	// ResourceMonitorInterval controls how frequently the resource monitor polls VMs
	ResourceMonitorInterval time.Duration
	// InstanceDomain is the domain for instance hostnames (e.g., exe.xyz, exe-staging.xyz)
	InstanceDomain string
	// ResourceManagerEnabled enables the resource manager service
	ResourceManagerEnabled bool
	// ResourceManagerInterval controls how frequently the resource manager polls VMs
	ResourceManagerInterval time.Duration
	// IdleThreshold is the duration after which a VM is considered idle
	IdleThreshold time.Duration
}
