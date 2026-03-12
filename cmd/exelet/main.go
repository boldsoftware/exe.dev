//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	cli "github.com/urfave/cli/v2"

	"exe.dev/deps/image"
	"exe.dev/exelet"
	"exe.dev/exelet/config"
	"exe.dev/exelet/desiredsync"
	"exe.dev/exelet/metadata"
	"exe.dev/exelet/network"
	"exe.dev/exelet/network/nat"
	"exe.dev/exelet/services"
	computeservice "exe.dev/exelet/services/compute"
	pktflowservice "exe.dev/exelet/services/pktflow"
	replicationservice "exe.dev/exelet/services/replication"
	resourcemanagerservice "exe.dev/exelet/services/resourcemanager"
	storageservice "exe.dev/exelet/services/storage"
	"exe.dev/exelet/storage"
	"exe.dev/logging"
	"exe.dev/stage"
	"exe.dev/version"
)

func main() {
	app := cli.NewApp()
	app.Name = "exelet"
	app.Version = version.BuildVersion()
	app.Authors = []*cli.Author{
		{
			Name: "exe.dev",
		},
	}
	app.Usage = version.Name + " (exelet)"
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "debug",
			Aliases: []string{"D"},
			Usage:   "enable debug logging",
		},
		&cli.BoolFlag{
			Name:  "log-json",
			Usage: "output logs in JSON format instead of text",
		},
		&cli.StringFlag{
			Name:    "name",
			Usage:   "exelet node name",
			Value:   "local",
			EnvVars: []string{"EXELET_NODE_NAME"},
		},
		&cli.StringFlag{
			Name:    "listen-address",
			Usage:   "listen address for the grpc server",
			Value:   config.DefaultExeletAddress,
			EnvVars: []string{"EXELET_LISTEN_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "data-dir",
			Usage:   "server data directory",
			Value:   "/var/tmp/exelet",
			EnvVars: []string{"EXELET_DATA_DIR"},
		},
		&cli.StringFlag{
			Name:  "region",
			Usage: "server locality region",
			Value: "us-central",
		},
		&cli.StringFlag{
			Name:  "zone",
			Usage: "server locality zone",
			Value: "1a",
		},
		&cli.StringFlag{
			Name:    "runtime-address",
			Usage:   "address to the exelet runtime",
			Value:   "cloudhypervisor:///var/tmp/exelet/runtime",
			EnvVars: []string{"EXELET_RUNTIME_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "network-manager-address",
			Usage:   "address to the exelet network manager",
			Value:   "nat:///var/tmp/exelet/network",
			EnvVars: []string{"EXELET_NETWORK_MANAGER_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "storage-manager-address",
			Usage:   "address to the exelet storage manager",
			Value:   "zfs:///var/tmp/exelet/storage?dataset=tank",
			EnvVars: []string{"EXELET_STORAGE_MANAGER_ADDRESS"},
		},
		&cli.BoolFlag{
			Name:    "enable-instance-boot-on-startup",
			Usage:   "enable starting local instances on server start",
			EnvVars: []string{"EXELET_INSTANCE_BOOT_ON_STARTUP"},
		},
		&cli.BoolFlag{
			Name:    "maintenance",
			Usage:   "set exelet state in maintenance mode (no new workloads)",
			EnvVars: []string{"EXELET_ENABLE_MAINTENANCE"},
		},
		&cli.StringFlag{
			Name:    "http-addr",
			Usage:   "HTTP server address for debug, metrics, and version endpoints",
			Value:   config.DefaultHTTPAddress,
			EnvVars: []string{"EXELET_HTTP_ADDRESS"},
		},
		&cli.IntFlag{
			Name:    "proxy-port-min",
			Usage:   "minimum port for proxy allocation (defaults to 10000)",
			Value:   10000,
			EnvVars: []string{"EXELET_PROXY_PORT_MIN"},
		},
		&cli.IntFlag{
			Name:    "proxy-port-max",
			Usage:   "maximum port for proxy allocation (defaults to 20000)",
			Value:   20000,
			EnvVars: []string{"EXELET_PROXY_PORT_MAX"},
		},
		&cli.StringFlag{
			Name:    "exed-url",
			Usage:   "URL of the exed HTTP(S) server (e.g., http://localhost:8080)",
			Value:   "",
			EnvVars: []string{"EXELET_EXED_URL"},
		},
		&cli.StringFlag{
			Name:    "metadata-url",
			Usage:   "URL of the local metadata HTTP(S) server, either exed or exeprox",
			Value:   "",
			EnvVars: []string{"EXELET_METADATA_URL", "EXELET_EXED_URL"},
		},
		&cli.BoolFlag{
			Name:    "desired-state-sync",
			Usage:   "enable periodic desired-state sync from exed (requires --exed-url)",
			Value:   true,
			EnvVars: []string{"EXELET_DESIRED_STATE_SYNC"},
		},
		&cli.DurationFlag{
			Name:    "desired-state-sync-interval",
			Usage:   "polling interval for desired-state sync (e.g., 5s, 1m)",
			Value:   desiredsync.DefaultPollInterval,
			EnvVars: []string{"EXELET_DESIRED_STATE_SYNC_INTERVAL"},
		},
		&cli.StringFlag{
			Name:    "instance-domain",
			Usage:   "domain for instance hostnames (e.g., exe.xyz, exe-staging.xyz)",
			Value:   config.DefaultInstanceDomain,
			EnvVars: []string{"EXELET_INSTANCE_DOMAIN"},
		},
		&cli.DurationFlag{
			Name:    "resource-manager-interval",
			Usage:   "polling interval for the resource manager (e.g., 30s, 1m)",
			Value:   config.DefaultResourceManagerInterval,
			EnvVars: []string{"EXELET_RESOURCE_MANAGER_INTERVAL"},
		},
		&cli.BoolFlag{
			Name:    "enable-hugepages",
			Usage:   "enable hugepage memory for VMs (requires hugepages to be configured on the host)",
			EnvVars: []string{"EXELET_ENABLE_HUGEPAGES"},
		},
		&cli.StringFlag{
			Name:    "proxy-bind-ip",
			Usage:   "IP address to bind SSH proxies to (empty means all interfaces, use Tailscale IP for production)",
			Value:   "",
			EnvVars: []string{"EXELET_PROXY_BIND_IP"},
		},
		&cli.StringFlag{
			Name:    "stage",
			Usage:   "deployment stage: prod, staging, local, or test",
			Value:   "",
			EnvVars: []string{"EXELET_STAGE"},
		},
		&cli.BoolFlag{
			Name:    "storage-replication-enabled",
			Usage:   "enable storage replication",
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_ENABLED"},
		},
		&cli.DurationFlag{
			Name:    "storage-replication-interval",
			Usage:   "interval between replication cycles (e.g., 1h, 30m)",
			Value:   config.DefaultReplicationInterval,
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_INTERVAL"},
		},
		&cli.StringFlag{
			Name:    "storage-replication-target",
			Usage:   "replication target URL (ssh://user@host/pool or file:///path)",
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_TARGET"},
		},
		&cli.StringFlag{
			Name:    "storage-replication-ssh-key",
			Usage:   "path to SSH private key (required for SSH targets)",
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_SSH_KEY"},
		},
		&cli.StringFlag{
			Name:    "storage-replication-ssh-command",
			Usage:   "use system SSH binary (e.g. \"ssh\") instead of built-in SSH client, enabling Tailscale SSH and system SSH config",
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_SSH_COMMAND"},
		},
		&cli.StringFlag{
			Name:    "storage-replication-known-hosts",
			Usage:   "path to known_hosts file for SSH host key verification (default: ~/.ssh/known_hosts)",
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_KNOWN_HOSTS"},
		},
		&cli.IntFlag{
			Name:    "storage-replication-retention",
			Usage:   "number of snapshots to keep on the target",
			Value:   config.DefaultReplicationRetention,
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_RETENTION"},
		},
		&cli.StringFlag{
			Name:    "storage-replication-bandwidth-limit",
			Usage:   "maximum transfer rate (e.g., 100M, 1G)",
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_BANDWIDTH_LIMIT"},
		},
		&cli.BoolFlag{
			Name:    "storage-replication-prune",
			Usage:   "remove orphaned backups from target",
			Value:   config.DefaultReplicationPrune,
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_PRUNE"},
		},
		&cli.IntFlag{
			Name:    "storage-replication-workers",
			Usage:   "number of concurrent replication workers (0 = auto: NumCPU/4, min 1)",
			Value:   0,
			EnvVars: []string{"EXELET_STORAGE_REPLICATION_WORKERS"},
		},
		&cli.StringFlag{
			Name:    "metrics-daemon-url",
			Usage:   "URL of the metrics daemon (e.g., http://localhost:8090)",
			EnvVars: []string{"EXELET_METRICS_DAEMON_URL"},
		},
		&cli.DurationFlag{
			Name:    "metrics-daemon-interval",
			Usage:   "interval for sending metrics to the daemon",
			Value:   config.DefaultMetricsDaemonInterval,
			EnvVars: []string{"EXELET_METRICS_DAEMON_INTERVAL"},
		},
		&cli.BoolFlag{
			Name:    "pktflow-enabled",
			Usage:   "enable pktflow collector",
			EnvVars: []string{"EXELET_PKTFLOW_ENABLED"},
		},
		&cli.DurationFlag{
			Name:    "pktflow-interval",
			Usage:   "pktflow collection interval (e.g., 5s, 10s)",
			EnvVars: []string{"EXELET_PKTFLOW_INTERVAL"},
			Value:   config.DefaultPktFlowInterval,
		},
		&cli.StringFlag{
			Name:    "pktflow-host-id",
			Usage:   "override host id for pktflow reports",
			EnvVars: []string{"EXELET_PKTFLOW_HOST_ID"},
		},
		&cli.DurationFlag{
			Name:    "pktflow-mapping-refresh",
			Usage:   "pktflow instance mapping refresh interval (e.g., 1m)",
			EnvVars: []string{"EXELET_PKTFLOW_MAPPING_REFRESH"},
		},
		&cli.UintFlag{
			Name:    "pktflow-sample-rate",
			Usage:   "pktflow packet sample rate (power of two, e.g., 1024)",
			EnvVars: []string{"EXELET_PKTFLOW_SAMPLE_RATE"},
			Value:   1024,
		},
		&cli.IntFlag{
			Name:    "pktflow-max-flows",
			Usage:   "max flow records per tap interval",
			EnvVars: []string{"EXELET_PKTFLOW_MAX_FLOWS"},
			Value:   200,
		},
		&cli.IntFlag{
			Name:    "reserved-cpus",
			Usage:   "number of CPUs to reserve for the host system via cpuset (0 to disable)",
			Value:   2,
			EnvVars: []string{"EXELET_RESERVED_CPUS"},
		},
	}
	app.Action = serveAction

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func serveAction(clix *cli.Context) error {
	name := clix.String("name")
	debug := clix.Bool("debug")
	listenAddress := clix.String("listen-address")
	dataDir := clix.String("data-dir")
	region := clix.String("region")
	zone := clix.String("zone")
	runtimeAddress := clix.String("runtime-address")
	networkManagerAddress := clix.String("network-manager-address")
	storageManagerAddress := clix.String("storage-manager-address")
	enableInstanceBootOnStartup := clix.Bool("enable-instance-boot-on-startup")
	stageName := clix.String("stage")
	if stageName == "" {
		return fmt.Errorf("--stage flag is required (prod, staging, local, or test)")
	}
	env, err := stage.Parse(stageName)
	if err != nil {
		return err
	}

	if debug {
		os.Setenv("LOG_LEVEL", "debug")
	}
	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metricsRegistry.MustRegister(prometheus.NewGoCollector())
	version.RegisterBuildInfo(metricsRegistry)
	logging.SetupLogger(env, metricsRegistry, &logging.ResourceAttrs{
		ServiceVersion: version.BuildVersion(),
		DeploymentEnv:  stageName,
	})
	log := slog.Default()

	maintenanceMode := clix.Bool("maintenance")
	httpAddr := clix.String("http-addr")
	proxyPortMin := clix.Int("proxy-port-min")
	proxyPortMax := clix.Int("proxy-port-max")
	exedURL := clix.String("exed-url")
	metadataURL := clix.String("metadata-url")
	if metadataURL == "" {
		metadataURL = exedURL
	}
	instanceDomain := clix.String("instance-domain")
	resourceManagerInterval := clix.Duration("resource-manager-interval")
	enableHugepages := clix.Bool("enable-hugepages")
	proxyBindIP := clix.String("proxy-bind-ip")
	replicationEnabled := clix.Bool("storage-replication-enabled")
	replicationInterval := clix.Duration("storage-replication-interval")
	replicationTarget := clix.String("storage-replication-target")
	replicationSSHKey := clix.String("storage-replication-ssh-key")
	replicationSSHCommand := clix.String("storage-replication-ssh-command")
	replicationKnownHosts := clix.String("storage-replication-known-hosts")
	replicationRetention := clix.Int("storage-replication-retention")
	replicationBandwidthLimit := clix.String("storage-replication-bandwidth-limit")
	replicationPrune := clix.Bool("storage-replication-prune")
	replicationWorkers := clix.Int("storage-replication-workers")
	metricsDaemonURL := clix.String("metrics-daemon-url")
	metricsDaemonInterval := clix.Duration("metrics-daemon-interval")
	pktflowEnabled := clix.Bool("pktflow-enabled")
	pktflowInterval := clix.Duration("pktflow-interval")
	pktflowHostID := clix.String("pktflow-host-id")
	pktflowMappingRefresh := clix.Duration("pktflow-mapping-refresh")
	pktflowSampleRate := clix.Uint("pktflow-sample-rate")
	pktflowMaxFlows := clix.Int("pktflow-max-flows")

	reservedCPUs := clix.Int("reserved-cpus")

	// Validate replication config
	if replicationEnabled && replicationTarget == "" {
		return fmt.Errorf("--storage-replication-target is required when replication is enabled")
	}
	if replicationEnabled && replicationWorkers < 0 {
		return fmt.Errorf("--storage-replication-workers must be >= 0 (0 = auto)")
	}
	if pktflowEnabled {
		if pktflowSampleRate == 0 || (pktflowSampleRate&(pktflowSampleRate-1)) != 0 {
			return fmt.Errorf("--pktflow-sample-rate must be a power of two")
		}
		if pktflowMaxFlows <= 0 {
			return fmt.Errorf("--pktflow-max-flows must be > 0")
		}
	}

	cfg := &config.ExeletConfig{
		Name:                        name,
		ListenAddress:               listenAddress,
		DataDir:                     dataDir,
		Region:                      region,
		Zone:                        zone,
		RuntimeAddress:              runtimeAddress,
		NetworkManagerAddress:       networkManagerAddress,
		StorageManagerAddress:       storageManagerAddress,
		EnableInstanceBootOnStartup: enableInstanceBootOnStartup,
		ProxyPortMin:                proxyPortMin,
		ProxyPortMax:                proxyPortMax,
		ExedURL:                     exedURL,
		MetadataURL:                 metadataURL,
		InstanceDomain:              instanceDomain,
		ResourceManagerInterval:     resourceManagerInterval,
		EnableHugepages:             enableHugepages,
		ProxyBindIP:                 proxyBindIP,
		ReplicationEnabled:          replicationEnabled,
		ReplicationInterval:         replicationInterval,
		ReplicationTarget:           replicationTarget,
		ReplicationSSHKey:           replicationSSHKey,
		ReplicationSSHCommand:       replicationSSHCommand,
		ReplicationKnownHostsPath:   replicationKnownHosts,
		ReplicationRetention:        replicationRetention,
		ReplicationBandwidthLimit:   replicationBandwidthLimit,
		ReplicationPrune:            replicationPrune,
		ReplicationWorkers:          replicationWorkers,
		MetricsDaemonURL:            metricsDaemonURL,
		MetricsDaemonInterval:       metricsDaemonInterval,
		ReservedCPUs:                reservedCPUs,
		PktFlowEnabled:              pktflowEnabled,
		PktFlowInterval:             pktflowInterval,
		PktFlowHostID:               pktflowHostID,
		PktFlowMappingRefresh:       pktflowMappingRefresh,
		PktFlowSampleRate:           uint32(pktflowSampleRate),
		PktFlowMaxFlows:             pktflowMaxFlows,
	}

	opts := []exelet.ServerOpt{
		exelet.WithMetricsRegistry(metricsRegistry),
	}
	if maintenanceMode {
		opts = append(opts, exelet.WithMaintenance())
	}
	srv, err := exelet.NewExelet(cfg, log, env, opts...)
	if err != nil {
		return err
	}

	// start HTTP server
	if _, err := srv.StartHTTPServer(httpAddr, srv.MetricsRegistry()); err != nil {
		return err
	}

	ctx := context.Background()

	// network
	nm, err := network.NewNetworkManager(cfg.NetworkManagerAddress, log)
	if err != nil {
		return err
	}
	// start network manager
	if err := nm.Start(ctx); err != nil {
		return err
	}

	// image manager
	contentStoreDir := filepath.Join(cfg.DataDir, "content")
	if err := os.MkdirAll(contentStoreDir, 0o770); err != nil {
		return err
	}
	im, err := image.NewImageManager(&image.Config{DataDir: contentStoreDir}, log)
	if err != nil {
		return err
	}

	// storage manager
	storageManager, err := storage.NewStorageManager(cfg.StorageManagerAddress, log)
	if err != nil {
		return err
	}

	// Create compute service
	computeSvc, err := computeservice.New(cfg, log)
	if err != nil {
		return err
	}

	// Create storage service first so it can be the ImageLoader
	storageSvc, err := storageservice.New(cfg, log)
	if err != nil {
		return err
	}

	serviceContext := &services.ServiceContext{
		StorageManager:  storageManager,
		NetworkManager:  nm,
		ImageManager:    im,
		ComputeService:  computeSvc.(*computeservice.Service),
		ImageLoader:     storageSvc.(*storageservice.Service),
		MetricsRegistry: srv.MetricsRegistry(),
	}

	svcs := []func(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error){
		func(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
			return computeSvc, nil
		},
		resourcemanagerservice.New,
		pktflowservice.New,
		func(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
			return storageSvc, nil
		},
		replicationservice.New,
	}

	if err := srv.Register(serviceContext, svcs); err != nil {
		return err
	}

	// Start metadata service after services are registered
	// Get the bridge IP to bind the metadata service to
	// This allows multiple exelets to run in parallel without port conflicts
	networkConfig := nm.Config(ctx)
	natConfig, ok := networkConfig.(*nat.Config)
	if !ok || natConfig == nil || natConfig.Router == "" {
		return fmt.Errorf("failed to get NAT configuration for metadata service")
	}

	// Bind to the bridge's router IP (usually .1 in the network)
	metadataListenAddr := natConfig.Router + ":80"
	log.InfoContext(ctx, "metadata service will bind to bridge IP", "addr", metadataListenAddr)

	// Build the list of integration host suffixes. The primary suffix is derived
	// from BoxHost (e.g., ".int.exe.xyz"). For backward compatibility, we also
	// accept the legacy ".int.exe.cloud" suffix.
	integrationSuffixes := []string{env.IntegrationHostSuffix()}
	if env.IntegrationHostSuffix() != ".int.exe.cloud" {
		integrationSuffixes = append(integrationSuffixes, ".int.exe.cloud")
	}
	metadataSvc, err := metadata.NewService(log, serviceContext.ComputeService, cfg.MetadataURL, metadataListenAddr, integrationSuffixes, env.GatewayDev, serviceContext.MetricsRegistry)
	if err != nil {
		return err
	}
	if env.GatewayDev {
		metadata.IntegrationCacheTTL = 2 * time.Second
	}
	if err := metadataSvc.Start(ctx); err != nil {
		return err
	}

	if err := srv.Run(ctx); err != nil {
		return err
	}

	// Start desired-state syncer if enabled.
	// Must be after srv.Run() so we can read the actual bound address.
	var dsSync *desiredsync.Syncer
	if clix.Bool("desired-state-sync") {
		if cfg.ExedURL == "" {
			return fmt.Errorf("--desired-state-sync requires --exed-url to be set")
		}
		// Parse the port from the actual bound address (handles port 0 in tests).
		actualURL, err := url.Parse(srv.ActualAddr())
		if err != nil {
			return fmt.Errorf("failed to parse actual address: %w", err)
		}
		_, port, _ := net.SplitHostPort(actualURL.Host)
		// Use --name (EXELET_NODE_NAME) so the address matches what exed
		// registers. --name must match the hostname in exed's
		// -exelet-addresses flag.
		exeletAddr := fmt.Sprintf("tcp://%s:%s", name, port)

		// Wire up the device resolver for IO throttling if storage is available.
		var deviceResolver desiredsync.DeviceResolver
		if storageManager != nil {
			deviceResolver = &desiredsync.StorageDeviceResolver{StorageManager: storageManager}
		}

		dsSync, err = desiredsync.New(desiredsync.Config{
			ExedURL:        cfg.ExedURL,
			ExeletAddr:     exeletAddr,
			PollInterval:   clix.Duration("desired-state-sync-interval"),
			DeviceResolver: deviceResolver,
		}, log)
		if err != nil {
			return fmt.Errorf("failed to create desired-state syncer: %w", err)
		}
		if err := dsSync.Start(ctx); err != nil {
			return fmt.Errorf("failed to start desired-state syncer: %w", err)
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	doneCh := make(chan bool, 1)

	go func() {
		for sig := range signals {
			switch sig {
			case syscall.SIGUSR1:
				log.DebugContext(ctx, "generating debug profile")
				profilePath, err := srv.GenerateProfile()
				if err != nil {
					log.ErrorContext(ctx, err.Error())
					continue
				}
				log.InfoContext(ctx, "generated memory profile", "path", profilePath)
			case syscall.SIGTERM, syscall.SIGINT:
				log.InfoContext(ctx, "shutting down")
				if dsSync != nil {
					dsSync.Stop()
				}
				stopCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
				if err := srv.Stop(stopCtx); err != nil {
					log.ErrorContext(ctx, err.Error())
				}
				cancel()
				doneCh <- true
			default:
				log.WarnContext(ctx, "unhandled signal", "signal", sig)
			}
		}
	}()

	<-doneCh

	return nil
}
