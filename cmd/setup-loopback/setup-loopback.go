package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
)

var (
	down = flag.Bool("d", false, "turn aliases down instead of up")
	from = flag.Int("from", 2, "start of address range [0-255]")
	to   = flag.Int("to", 255, "end (inclusive) of address range [0-255]")
)

// This utility creates a range of loopback IP aliases for 127.0.0.xx.
// On MacOS at least, by default you can't listen on loopback addresses
// besides 127.0.0.1. Running this command once will create a range of
// loopback addresses that exed can listen on locally, and bind to
// multicast DNS names.  This ssh and other apps can resolve host names
// like machine.team.exe.local to speicific IP addresses within the
// 127.0.0.xx range created here.
func main() {
	flag.Parse()
	if os.Geteuid() != 0 {
		fmt.Printf("Must command can only succeed when executed as root.\n")
		os.Exit(1)
	}
	dir := "up"
	if *down {
		dir = "down"
	}
	addrs := getLoopbackAddresses(*from, *to, *down)
	fmt.Printf("turned %d loopback aliases %s\n", len(addrs), dir)
	for _, addr := range addrs {
		fmt.Printf("%s\n", addr)
	}
}

func getLoopbackAddresses(from, to int, down bool) []string {
	ret := []string{}
	for i := range to - from + 1 {
		addr := fmt.Sprintf("127.0.0.%0d", from+i)
		subCmd := "alias"
		if down {
			subCmd = "-alias"
		}
		cmd := exec.Command("ifconfig", "lo0", subCmd, addr)
		stout, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("error from %q:\n%s\n", cmd.Args, err.Error())
			continue
		}
		if len(stout) != 0 {
			fmt.Printf("output from %q:\n%s\n", cmd.Args, string(stout))
		}
		if !down {
			lis, err := net.Listen("tcp4", addr+":0")
			if err != nil {
				fmt.Printf("can't listen on %s: %s\n", addr, err.Error())
				continue
			}
			lis.Close()
		}
		ret = append(ret, addr)
	}
	return ret
}
