package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/sirupsen/logrus"
	cli "github.com/urfave/cli/v2"

	"exe.dev/exelet"
	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	computeservice "exe.dev/exelet/services/compute"
	"exe.dev/exelet/storage"
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
			Name:  "pprof-addr",
			Usage: "performance profiling address",
			Value: "",
		},
	}
	app.Action = serveAction

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func serveAction(clix *cli.Context) error {
	debug := clix.Bool("debug")

	name := clix.String("name")
	listenAddress := clix.String("listen-address")
	dataDir := clix.String("data-dir")
	region := clix.String("region")
	zone := clix.String("zone")
	runtimeAddress := clix.String("runtime-address")
	networkManagerAddress := clix.String("network-manager-address")
	storageManagerAddress := clix.String("storage-manager-address")
	enableInstanceBootOnStartup := clix.Bool("enable-instance-boot-on-startup")

	hOpts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	if debug {
		hOpts.Level = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, hOpts))

	maintenanceMode := clix.Bool("maintenance")
	pprofAddr := clix.String("pprof-addr")
	if pprofAddr != "" {
		go func() {
			log.Info("starting pprof server", "addr", pprofAddr)
			if err := http.ListenAndServe(pprofAddr, nil); err != nil {
				log.Error("error starting pprof server", "err", err)
			}
		}()
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
	}

	opts := []exelet.ServerOpt{}
	if maintenanceMode {
		opts = append(opts, exelet.WithMaintenance())
	}
	srv, err := exelet.NewExelet(cfg, log, opts...)
	if err != nil {
		return err
	}

	svcs := []func(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error){
		computeservice.New,
	}

	// storage
	storageManager, err := storage.NewStorageManager(cfg.StorageManagerAddress, log)
	if err != nil {
		return err
	}

	serviceContext := &services.ServiceContext{
		StorageManager: storageManager,
	}

	if err := srv.Register(serviceContext, svcs); err != nil {
		return err
	}

	ctx := context.Background()
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
