package vpctunnel

import (
	"io"
	"net"
	"sync"
)

// Splice copies bytes in both directions until both streams reach EOF. It
// half-closes each destination after its source finishes.
func Splice(left, right net.Conn) (leftToRight, rightToLeft int64) {
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		leftToRight, _ = io.Copy(right, left)
		closeWrite(right)
	}()
	go func() {
		defer wait.Done()
		rightToLeft, _ = io.Copy(left, right)
		closeWrite(left)
	}()
	wait.Wait()
	return leftToRight, rightToLeft
}

func closeWrite(connection net.Conn) {
	if closer, ok := connection.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	_ = connection.Close()
}
