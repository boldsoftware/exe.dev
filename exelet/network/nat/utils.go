//go:build linux

package nat

import (
	"crypto/rand"
	"fmt"

	"exe.dev/exelet/utils"
)

func getTapID(id string) string {
	return utils.GetTapName(id)
}

// getIfbName returns the IFB device name for a given TAP device.
// IFB devices are used to redirect TAP ingress for bandwidth shaping.
func getIfbName(tapName string) string {
	// Replace "tap-" prefix with "ifb-"
	if len(tapName) > 4 && tapName[:4] == "tap-" {
		return "ifb-" + tapName[4:]
	}
	return "ifb-" + tapName
}

func randomMAC() (string, error) {
	buf := make([]byte, 6)
	_, err := rand.Read(buf)
	if err != nil {
		return "", err
	}
	buf[0] = 0b10 // unicast
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0],
		buf[1],
		buf[2],
		buf[3],
		buf[4],
		buf[5],
	), nil
}
