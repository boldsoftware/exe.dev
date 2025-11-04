package main

import (
	"strconv"
	"strings"
)

type netConfig struct {
	IP               string
	Server           string
	Gateway          string
	Netmask          string
	Hostname         string
	Device           string
	Conf             string
	Nameserver       string
	BackupNameserver string
	NTPServer        string
}

func parseNetConf(conf string) (*netConfig, error) {
	// ip=<client-ip>:<srv-ip>:<gw-ip>:<netmask>:<host>:<device>:<autoconf>:<dns0-ip>:<dns1-ip>:<ntp0-ip>
	options := strings.Split(conf, ":")
	if len(options) == 0 {
		return nil, nil
	}

	cfg := &netConfig{}

	for i, v := range options {
		switch i {
		case 0:
			cfg.IP = v
		case 1:
			cfg.Server = v
		case 2:
			cfg.Gateway = v
		case 3:
			cfg.Netmask = v
		case 4:
			cfg.Hostname = v
		case 5:
			cfg.Device = v
		case 6:
			cfg.Conf = v
		case 7:
			cfg.Nameserver = v
		case 8:
			cfg.BackupNameserver = v
		case 9:
			cfg.NTPServer = v
		}
	}

	return cfg, nil
}

// this is to detect an optimized exe.dev environment and use its init
func isExeDevConfigured() bool {
	v, err := getBootArg("use-exetini")
	if err != nil {
		return false
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return enabled
}
