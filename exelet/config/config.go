package config

import "time"

const (
	// DefaultServerAddress is the default exelet server address
	DefaultExeletAddress = "tcp://127.0.0.1:9080"

	// DefaultHTTPAddress is the default HTTP server address for debug and metrics
	DefaultHTTPAddress = ":9081"
	// DefaultResourceManagerInterval is the default polling interval for the resource manager
	DefaultResourceManagerInterval = 30 * time.Second

	// DefaultBootLogRotationInterval is the default interval for checking boot log sizes
	DefaultBootLogRotationInterval = 1 * time.Minute
	// DefaultBootLogMaxBytes is the maximum size before rotation is triggered (1 MB)
	DefaultBootLogMaxBytes = 1 << 20
	// DefaultBootLogKeepBytes is how many bytes to keep after rotation (32 KB)
	DefaultBootLogKeepBytes = 32 * 1024

	// DefaultReplicationInterval is the default interval for storage replication
	DefaultReplicationInterval = 1 * time.Hour
	// DefaultReplicationRetention is the default number of snapshots to keep
	DefaultReplicationRetention = 24
	// DefaultReplicationPrune enables pruning orphaned backups by default
	DefaultReplicationPrune = true

	// DefaultMetricsDaemonInterval is the default interval for sending metrics to the daemon
	DefaultMetricsDaemonInterval = 10 * time.Minute

	// DefaultPktFlowInterval is the default collection interval for pktflow reports
	DefaultPktFlowInterval = 5 * time.Second

	// DefaultNameserver is the default instance nameserver
	DefaultNameserver = "1.1.1.1"

	// DefaultStorageTierMigrationWorkers is the default number of concurrent tier migrations
	DefaultStorageTierMigrationWorkers = 1

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
	// StorageTiers is a list of additional storage pool addresses
	// (same URL format as StorageManagerAddress, e.g., "zfs:///var/tmp/exelet/storage?dataset=nvme").
	// The pool specified by StorageManagerAddress is always the primary tier.
	StorageTiers []string
	// StorageTierMigrationWorkers is the maximum number of concurrent tier migrations (default 1)
	StorageTierMigrationWorkers int
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
	// MetadataURL is the URL of the metadata server,
	// either exed or exeprox.
	MetadataURL string
	// ExepipeAddress is the Unix domain address of the exepipe server.
	ExepipeAddress string
	// InstanceDomain is the domain for instance hostnames (e.g., exe.xyz, exe-staging.xyz)
	InstanceDomain string
	// ResourceManagerInterval controls how frequently the resource manager polls VMs
	ResourceManagerInterval time.Duration
	// EnableHugepages enables hugepage memory for VMs (requires hugepages to be configured on the host)
	EnableHugepages bool
	// ProxyBindIP is the IP address to bind SSH proxies to (empty means all interfaces)
	ProxyBindIP string
	// BootLogRotationInterval controls how frequently boot logs are checked for rotation
	BootLogRotationInterval time.Duration
	// BootLogMaxBytes is the maximum size for boot logs before rotation is triggered
	BootLogMaxBytes int64
	// BootLogKeepBytes is how many bytes to keep after rotation
	BootLogKeepBytes int64

	// BackupPoolFallback allows PoolForInstance to resolve instances from the
	// backup pool when they don't exist on any primary storage tier. Disabled by
	// default to prevent accidental runs from backup storage.
	BackupPoolFallback bool

	// ReplicationEnabled enables storage replication
	ReplicationEnabled bool
	// ReplicationInterval is the interval between replication cycles
	ReplicationInterval time.Duration
	// ReplicationTarget is the target URL (ssh://user@host/pool or file:///path)
	ReplicationTarget string
	// ReplicationSSHKey is the path to the SSH private key for SSH targets
	ReplicationSSHKey string
	// ReplicationSSHCommand is the path to the system SSH binary (e.g. "ssh").
	// When set, uses the system SSH binary instead of Go's built-in SSH client,
	// which allows Tailscale SSH, ProxyCommand, and other system SSH config to work.
	ReplicationSSHCommand string
	// ReplicationKnownHostsPath is the path to known_hosts for SSH host key verification (empty uses ~/.ssh/known_hosts)
	ReplicationKnownHostsPath string
	// ReplicationRetention is the number of snapshots to keep on the target
	ReplicationRetention int
	// ReplicationBandwidthLimit is the maximum transfer rate (e.g., "100M", "1G")
	ReplicationBandwidthLimit string
	// ReplicationPrune enables pruning orphaned backups from the target
	ReplicationPrune bool
	// ReplicationWorkers is the number of concurrent replication workers (0 = auto: NumCPU/4, min 1)
	ReplicationWorkers int

	// MetricsDaemonURL is the URL of the metrics daemon (e.g., http://localhost:8090)
	MetricsDaemonURL string
	// MetricsDaemonInterval is the interval for sending metrics to the daemon
	MetricsDaemonInterval time.Duration

	// ReservedCPUs is the number of CPUs to reserve for the host system.
	// When > 0, cpuset.cpus on the exelet.slice will be set to exclude the
	// first N cores (e.g., ReservedCPUs=2 on a 64-core machine → cpuset.cpus="2-63").
	ReservedCPUs int

	// PktFlowEnabled enables the pktflow collector
	PktFlowEnabled bool
	// PktFlowInterval is the collection interval for pktflow reports
	PktFlowInterval time.Duration
	// PktFlowHostID overrides the host id reported to pktflow
	PktFlowHostID string
	// PktFlowMappingRefresh controls how often instance mappings are refreshed
	PktFlowMappingRefresh time.Duration
	// PktFlowSampleRate controls packet sampling (power of two, e.g., 1024)
	PktFlowSampleRate uint32
	// PktFlowMaxFlows caps the number of flow records per tap interval
	PktFlowMaxFlows int
}
