package instances

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var createInstanceCommand = &cli.Command{
	Name:  "create",
	Usage: "Create a new compute instance",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:        "id",
			Usage:       "id of the instance",
			DefaultText: "generated",
		},
		&cli.StringFlag{
			Name:    "name",
			Aliases: []string{"n"},
			Usage:   "name of the instance",
		},
		&cli.StringFlag{
			Name:    "image",
			Aliases: []string{"i"},
			Usage:   "image to use for the instance",
			Value:   "docker.io/library/alpine:latest",
		},
		&cli.Uint64Flag{
			Name:    "cpus",
			Aliases: []string{"c"},
			Usage:   "number of CPUs for the instance",
			Value:   uint64(1),
		},
		&cli.StringFlag{
			Name:    "memory",
			Aliases: []string{"m"},
			Usage:   "amount of memory for the instance",
			Value:   "1G",
		},
		&cli.StringFlag{
			Name:    "disk",
			Aliases: []string{"d"},
			Usage:   "amount of disk storage for the instance",
			Value:   "4G",
		},
		&cli.StringSliceFlag{
			Name:    "env",
			Aliases: []string{"e"},
			Usage:   "set environment variable for the instance (KEY=val)",
		},
		&cli.StringSliceFlag{
			Name:  "ssh-key",
			Usage: "public SSH key for instance",
		},
		&cli.StringSliceFlag{
			Name:    "boot-arg",
			Aliases: []string{"b"},
			Usage:   "instance boot args",
		},
		&cli.StringSliceFlag{
			Name:    "volume",
			Aliases: []string{"v"},
			Usage:   "add instance volume (type=<type>,source=<source>,mountpoint=<guest-path>)",
		},
		&cli.StringSliceFlag{
			Name:  "config",
			Usage: "add instance config (type=<type>,source=<local-path>,destination=<guest-path>)",
		},
	},
	Action: func(clix *cli.Context) error {
		id := clix.String("id")
		name := clix.String("name")
		if name == "" {
			h := sha256.New()
			h.Write([]byte(time.Now().String()))
			n := fmt.Sprintf("%x", h.Sum(nil))
			name = n[:12]
		}
		image := clix.String("image")
		cpus := clix.Uint64("cpus")
		memory := clix.String("memory")
		bootArgs := clix.StringSlice("boot-arg")
		env := clix.StringSlice("env")
		mem, err := humanize.ParseBytes(memory)
		if err != nil {
			return err
		}
		disk, err := humanize.ParseBytes(clix.String("disk"))
		if err != nil {
			return err
		}
		sshKeyPaths := clix.StringSlice("ssh-key")
		sshKeys := []string{}
		for _, sshKeyPath := range sshKeyPaths {
			data, err := os.ReadFile(sshKeyPath)
			if err != nil {
				return fmt.Errorf("error reading ssh key %s: %w", sshKeyPath, err)
			}
			sshKeys = append(sshKeys, string(data))
		}
		volumes, err := parseVolumes(clix.StringSlice("volume"))
		if err != nil {
			return err
		}
		configs, err := parseConfigs(clix.StringSlice("config"))
		if err != nil {
			return err
		}

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		req := &api.CreateInstanceRequest{
			ID:       id,
			Name:     name,
			Image:    image,
			CPUs:     cpus,
			Memory:   mem,
			Disk:     disk,
			SSHKeys:  sshKeys,
			Env:      env,
			BootArgs: bootArgs,
			Volumes:  volumes,
			Configs:  configs,
		}

		// progress
		s := helpers.NewSpinner("creating instance")
		s.Start()
		defer s.Stop()

		ctx := context.Background()
		stream, err := c.CreateInstance(ctx, req)
		if err != nil {
			s.Stop()
			return err
		}

		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}
			switch v := resp.Type.(type) {
			case *api.CreateInstanceResponse_Status:
				s.Update(v.Status.Message)
			case *api.CreateInstanceResponse_Instance:
				s.Final(v.Instance.ID)
				s.Stop()
			}
		}

		return nil
	},
}

func parseVolumes(specs []string) ([]*api.Volume, error) {
	vols := []*api.Volume{}
	// parse type=t,source=foo,mountpoint=/bar
	for _, spec := range specs {
		sp := strings.Split(spec, ",")
		volType := ""
		source := ""
		mountpoint := ""
		for _, s := range sp {
			parts := strings.SplitN(s, "=", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid volume format (expected type=<type>,source=<source,mountpoint=<guest-mountpoint>)")
			}
			key := strings.ToLower(parts[0])
			val := strings.ToLower(parts[1])
			switch key {
			case "type":
				volType = val
			case "source", "src":
				source = val
			case "mountpoint", "target":
				mountpoint = val
			default:
				return nil, fmt.Errorf("unsupported key in volume spec: %s", key)
			}
		}
		if volType == "" || source == "" || mountpoint == "" {
			return nil, fmt.Errorf("type, source and mountpoint must be specified for volume")
		}

		vols = append(vols, &api.Volume{
			Type:       volType,
			Source:     source,
			Mountpoint: mountpoint,
		})
	}

	return vols, nil
}

func parseConfigs(specs []string) ([]*api.Config, error) {
	configs := []*api.Config{}
	// parse type=file,source=/local/path,destination=/path/in/guest/foo
	for _, spec := range specs {
		sp := strings.Split(spec, ",")
		cfgType := ""
		data := []byte{}
		destination := ""
		mode := uint64(0o644)
		for _, s := range sp {
			parts := strings.SplitN(s, "=", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid config format (expected type=<type>,source=</local/path>,destination=<guest-path>,mode=<mode>)")
			}
			key := strings.ToLower(parts[0])
			val := strings.ToLower(parts[1])
			switch key {
			case "type":
				cfgType = val
			case "source", "data":
				d, err := os.ReadFile(val)
				if err != nil {
					return nil, err
				}
				data = d
			case "destination", "target":
				destination = val
			case "mode":
				m, err := strconv.ParseUint(val, 8, 32)
				if err != nil {
					return nil, err
				}
				mode = m
			default:
				return nil, fmt.Errorf("unsupported key in config spec: %s", key)
			}
		}
		if cfgType == "" || data == nil || destination == "" {
			return nil, fmt.Errorf("type, content and destination must be specified for config")
		}

		switch strings.ToLower(cfgType) {
		case "file":
			configs = append(configs, &api.Config{
				Destination: destination,
				Mode:        mode,
				Source: &api.Config_File{
					File: &api.FileConfig{
						Data: data,
					},
				},
			})
		default:
			return nil, fmt.Errorf("unsupported config type: %s", cfgType)
		}

	}

	return configs, nil
}
