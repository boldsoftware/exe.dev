package cloudhypervisor

import (
	"os"
	"strings"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

type virtiofsInstance struct {
	tag        string
	socketPath string
}

// toVmConfig convirts a exe VMConfig into a native CloudHypervisor client VmConfig
func (v *VMM) toVmConfig(cfg *api.VMConfig, virtiofsInstances []*virtiofsInstance) (client.VmConfig, error) {
	kernelPath := cfg.KernelPath
	args := strings.Join(cfg.Args, " ")
	memory := cfg.Memory
	// align memory
	if memDiff := memory % uint64(os.Getpagesize()); memDiff > 0 {
		memory = (cfg.Memory - memDiff)
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
	sharedMemory := true
	vCfg := client.VmConfig{
		Cpus: &client.CpusConfig{
			BootVcpus: int(cfg.CPUs),
			MaxVcpus:  int(cfg.CPUs),
		},
		Memory: &client.MemoryConfig{
			Size:   int64(memory),
			Shared: &sharedMemory,
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
