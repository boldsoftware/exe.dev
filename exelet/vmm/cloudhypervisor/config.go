package cloudhypervisor

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

type virtiofsInstance struct {
	tag        string
	socketPath string
}

// toVmConfig converts a exe VMConfig into a native CloudHypervisor client VmConfig
func (v *VMM) toVmConfig(cfg *api.VMConfig, virtiofsInstances []*virtiofsInstance) (*client.VmConfig, error) {
	kernelPath := cfg.KernelPath
	// Build cmdline: base args + network config derived from NetworkInterface
	// Filter out any stored ip= args (legacy instances or custom boot args) since
	// network config is always derived from NetworkInterface at runtime
	var filteredArgs []string
	for _, arg := range cfg.Args {
		if !strings.HasPrefix(arg, "ip=") {
			filteredArgs = append(filteredArgs, arg)
		}
	}
	args := strings.Join(filteredArgs, " ")
	if netConf := getNetConf(cfg.Name, cfg.NetworkInterface); netConf != "" {
		args = args + " " + netConf
	}
	memory := cfg.Memory
	vmMemory := memory
	// align memory to page size (hugepage size if enabled, otherwise default 4KB)
	if v.enableHugepages {
		hugePagesSize, err := defaultHugepageSize()
		if err != nil {
			return nil, fmt.Errorf("error getting default hugepage size (ensure hugepages are enabled): %w", err)
		}
		vmMemory = alignMemory(memory, uint64(hugePagesSize))
	} else {
		// Align to default page size (4KB) when not using hugepages
		vmMemory = alignMemory(memory, 4) // 4 * 1024 = 4096 bytes
	}
	rootDiskID := "root"
	disks := []client.DiskConfig{
		{
			Id:   &rootDiskID,
			Path: &cfg.RootDiskPath,
		},
	}
	networkConfig := []client.NetConfig{}
	if v := cfg.NetworkInterface; v != nil {
		networkConfig = []client.NetConfig{
			{
				Id:  &v.DeviceName,
				Mac: &v.MACAddress,
				Tap: &v.Name,
			},
		}
	}
	fs := []client.FsConfig{}
	for _, i := range virtiofsInstances {
		fs = append(fs, client.FsConfig{
			Id:        &i.tag,
			Tag:       i.tag,
			Socket:    i.socketPath,
			NumQueues: 1,
			QueueSize: 64,
		})
	}

	// Using virtiofs requires using shared memory.
	// When not using shared memory, enable KSM.
	sharedMemory := len(virtiofsInstances) > 0
	mergeableMemory := len(virtiofsInstances) == 0

	vCfg := &client.VmConfig{
		Cpus: &client.CpusConfig{
			BootVcpus: int(cfg.CPUs),
			MaxVcpus:  int(cfg.CPUs),
		},
		Memory: &client.MemoryConfig{
			Size:      int64(vmMemory),
			Shared:    &sharedMemory,
			Mergeable: &mergeableMemory,
			Hugepages: &v.enableHugepages,
		},
		Disks: &disks,
		// TODO: use console config to attach to stdin/stdout?
		Console: &client.ConsoleConfig{
			Mode: client.ConsoleConfigModeTty,
		},
		Net: &networkConfig,
		Payload: client.PayloadConfig{
			Kernel:  &kernelPath,
			Cmdline: &args,
		},
		Fs: &fs,
	}
	if v := cfg.InitramfsPath; v != "" {
		vCfg.Payload.Initramfs = &v
	}
	return vCfg, nil
}

func defaultHugepageSize() (int, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "Hugepagesize:") {
			fields := strings.Fields(l)
			if len(fields) >= 2 {
				size, err := strconv.Atoi(fields[1])
				if err != nil {
					return -1, err
				}
				return size, nil
			}
		}
	}
	return -1, fmt.Errorf("unable to get default huge page size")
}

func alignMemory(sizeBytes, hugepageSizeBytes uint64) uint64 {
	pageSize := hugepageSizeBytes * 1024
	return (sizeBytes / pageSize) * pageSize
}

// getNetConf generates the kernel ip= boot argument from the network interface config.
// Format: ip=<client-ip>:<srv-ip>:<gw-ip>:<netmask>:<host>:<device>:<autoconf>:<dns0-ip>:<dns1-ip>:<ntp0-ip>
func getNetConf(hostname string, iface *api.NetworkInterface) string {
	if iface == nil || iface.IP == nil || iface.IP.IPV4 == "" {
		return ""
	}

	ipSubnet := iface.IP.IPV4
	gw := iface.IP.GatewayV4
	iIP, ipnet, err := net.ParseCIDR(ipSubnet)
	if err != nil {
		return ""
	}
	netmask := net.IP(ipnet.Mask).String()
	ip := iIP.String()

	device := iface.DeviceName
	primaryNS := "1.1.1.1"
	backupNS := "8.8.8.8"
	switch len(iface.Nameservers) {
	case 0:
	case 1:
		primaryNS = iface.Nameservers[0]
	default:
		primaryNS = iface.Nameservers[0]
		backupNS = iface.Nameservers[1]
	}
	ntpServer := iface.NTPServer

	return fmt.Sprintf("ip=%s:%s:%s:%s:%s:%s:%s:%s:%s:%s",
		ip,
		gw,
		gw,
		netmask,
		hostname,
		device,
		"none",
		primaryNS,
		backupNS,
		ntpServer,
	)
}
