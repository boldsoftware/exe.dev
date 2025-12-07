package nat

import (
	"crypto/rand"
	"fmt"

	"exe.dev/exelet/utils"
)

func getTapID(id string) string {
	return utils.GetTapName(id)
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
