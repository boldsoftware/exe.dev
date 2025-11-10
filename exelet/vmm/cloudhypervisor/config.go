package cloudhypervisor

import (
	"bufio"
	"fmt"
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

// toVmConfig convirts a exe VMConfig into a native CloudHypervisor client VmConfig
func (v *VMM) toVmConfig(cfg *api.VMConfig, virtiofsInstances []*virtiofsInstance) (*client.VmConfig, error) {
	kernelPath := cfg.KernelPath
	args := strings.Join(cfg.Args, " ")
	memory := cfg.Memory
	// align memory
	hugePagesSize, err := defaultHugepageSize()
	if err != nil {
		// TODO: should we default to just disabling instead of returning an error?
		return nil, fmt.Errorf("error getting default hugepage size (ensure hugepages are enabled): %w", err)
	}
	vmMemory := alignMemory(memory, uint64(hugePagesSize))
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
	sharedMemory := true
	hugePages := true
	vCfg := &client.VmConfig{
		Cpus: &client.CpusConfig{
			BootVcpus: int(cfg.CPUs),
			MaxVcpus:  int(cfg.CPUs),
		},
		Memory: &client.MemoryConfig{
			Size:      int64(vmMemory),
			Shared:    &sharedMemory,
			Hugepages: &hugePages,
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
