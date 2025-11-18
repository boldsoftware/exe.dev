package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/sirupsen/logrus"
	cli "github.com/urfave/cli/v2"

	"exe.dev/deps/image"
	"exe.dev/exelet"
	"exe.dev/exelet/config"
	"exe.dev/exelet/metadata"
	"exe.dev/exelet/network"
	"exe.dev/exelet/services"
	computeservice "exe.dev/exelet/services/compute"
	storageservice "exe.dev/exelet/services/storage"
	"exe.dev/exelet/storage"
	"exe.dev/logging"
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
			Name:  "name",
			Usage: "exelet node name",
			Value: "local",
		},
		&cli.StringFlag{
			Name:  "listen-address",
			Usage: "listen address for the grpc server",
			Value: config.DefaultExeletAddress,
		},
		&cli.StringFlag{
			Name:  "data-dir",
			Usage: "server data directory",
			Value: "/var/tmp/exelet",
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
			Name:  "runtime-address",
			Usage: "address to the exelet runtime",
			Value: "cloudhypervisor:///var/tmp/exelet/runtime",
		},
		&cli.StringFlag{
			Name:  "network-manager-address",
			Usage: "address to the exelet network manager",
			Value: "nat:///var/tmp/exelet/network",
		},
		&cli.StringFlag{
			Name:  "storage-manager-address",
			Usage: "address to the exelet storage manager",
			Value: "zfs:///var/tmp/exelet/storage?dataset=tank",
		},
		&cli.BoolFlag{
			Name:  "enable-instance-boot-on-startup",
			Usage: "enable starting local instances on server start",
		},
		&cli.BoolFlag{
			Name:  "maintenance",
			Usage: "set exelet state in maintenance mode (no new workloads)",
		},
		&cli.StringFlag{
			Name:  "http-addr",
			Usage: "HTTP server address for debug, metrics, and version endpoints",
			Value: config.DefaultHTTPAddress,
		},
		&cli.IntFlag{
			Name:  "proxy-port-min",
			Usage: "minimum port for proxy allocation (defaults to 10000)",
			Value: 10000,
		},
		&cli.IntFlag{
			Name:  "proxy-port-max",
			Usage: "maximum port for proxy allocation (defaults to 20000)",
			Value: 20000,
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

	if debug {
		os.Setenv("LOG_LEVEL", "debug")
	}
	logging.SetupLogger("")
	log := slog.Default()

	maintenanceMode := clix.Bool("maintenance")
	httpAddr := clix.String("http-addr")
	proxyPortMin := clix.Int("proxy-port-min")
	proxyPortMax := clix.Int("proxy-port-max")

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
	}

	opts := []exelet.ServerOpt{}
	if maintenanceMode {
		opts = append(opts, exelet.WithMaintenance())
	}
	srv, err := exelet.NewExelet(cfg, log, opts...)
	if err != nil {
		return err
	}

	// start HTTP server
	if err := srv.StartHTTPServer(httpAddr, srv.MetricsRegistry()); err != nil {
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

	serviceContext := &services.ServiceContext{
		StorageManager: storageManager,
		NetworkManager: nm,
		ImageManager:   im,
		ComputeService: computeSvc.(*computeservice.Service),
	}

	svcs := []func(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error){
		func(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
			return computeSvc, nil
		},
		storageservice.New,
	}

	if err := srv.Register(serviceContext, svcs); err != nil {
		return err
	}

	// Start metadata service after services are registered
	metadataSvc := metadata.NewService(log, serviceContext.ComputeService)
	if err := metadataSvc.Start(ctx); err != nil {
		return err
	}

	if err := srv.Run(ctx); err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	doneCh := make(chan bool, 1)

	go func() {
		for sig := range signals {
			switch sig {
			case syscall.SIGUSR1:
				log.Debug("generating debug profile")
				profilePath, err := srv.GenerateProfile()
				if err != nil {
					log.Error(err.Error())
					continue
				}
				log.Info("generated memory profile", "path", profilePath)
			case syscall.SIGTERM, syscall.SIGINT:
				log.Info("shutting down")
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
				defer cancel()
				if err := srv.Stop(ctx); err != nil {
					log.Error(err.Error())
				}
				doneCh <- true
			default:
				log.Warn("unhandled signal", "signal", sig)
			}
		}
	}()

	<-doneCh

	return nil
}
