package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseNetConf(t *testing.T) {
	ip := "1.2.3.4"
	srv := "1.1.1.1"
	gw := "1.2.3.1"
	netmask := "255.255.255.0"
	host := "foo"
	device := "eth0"
	config := "none"
	ns := "8.8.8.8"
	backupNS := "8.8.4.4"
	ntp := "4.3.2.1"

	conf := fmt.Sprintf("%s:%s:%s:%s:%s:%s:%s:%s:%s:%s", ip, srv, gw, netmask, host, device, config, ns, backupNS, ntp)
	netConf, err := parseNetConf(conf)
	assert.NoError(t, err)

	assert.Equal(t, netConf.IP, ip)
	assert.Equal(t, netConf.Server, srv)
	assert.Equal(t, netConf.Gateway, gw)
	assert.Equal(t, netConf.Netmask, netmask)
	assert.Equal(t, netConf.Hostname, host)
	assert.Equal(t, netConf.Device, device)
	assert.Equal(t, netConf.Conf, config)
	assert.Equal(t, netConf.Nameserver, ns)
	assert.Equal(t, netConf.BackupNameserver, backupNS)
	assert.Equal(t, netConf.NTPServer, ntp)
}

func TestParseNetConfDHCP(t *testing.T) {
	conf := "dhcp"
	netConf, err := parseNetConf(conf)
	assert.NoError(t, err)
	assert.Equal(t, netConf.IP, "dhcp")
}

func TestParseNetConfBlank(t *testing.T) {
	hostname := "30c14e290e90"
	conf := "dhcp"
	c := fmt.Sprintf("::::%s::%s:1.1.1.1:8.8.8.8:ntp.ubuntu.com", hostname, conf)
	cfg, err := parseNetConf(c)
	assert.NoError(t, err)
	assert.Equal(t, cfg.Hostname, hostname)
	assert.Equal(t, cfg.Conf, conf)
}
