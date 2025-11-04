package nat

import (
	"crypto/rand"
	"fmt"

	"exe.dev/exelet/utils"
)

func getTapID(v ...string) string {
	return fmt.Sprintf("tap-%s", utils.GetID(v...)[:6])
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
