//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

func readTerminalLine(ctx context.Context, fd int, hidden bool) (line []byte, retErr error) {
	var original *unix.Termios
	if hidden {
		state, err := getTerminalState(fd)
		if err != nil {
			return nil, err
		}
		original = state
		hiddenState := *state
		hiddenState.Lflag &^= unix.ECHO
		hiddenState.Lflag |= unix.ICANON | unix.ISIG
		hiddenState.Iflag |= unix.ICRNL
		if err := setTerminalState(fd, &hiddenState); err != nil {
			return nil, err
		}
		defer func() {
			retErr = errors.Join(retErr, setTerminalState(fd, original))
		}()
	}

	cancelPipe := []int{0, 0}
	if err := unix.Pipe(cancelPipe); err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(cancelPipe[1], true); err != nil {
		unix.Close(cancelPipe[0])
		unix.Close(cancelPipe[1])
		return nil, err
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_, _ = unix.Write(cancelPipe[1], []byte{1})
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-stopped
		unix.Close(cancelPipe[0])
		unix.Close(cancelPipe[1])
	}()

	pollFDs := []unix.PollFd{
		{Fd: int32(fd), Events: unix.POLLIN},
		{Fd: int32(cancelPipe[0]), Events: unix.POLLIN},
	}
	for {
		_, err := unix.Poll(pollFDs, -1)
		if errors.Is(err, unix.EINTR) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if pollFDs[1].Revents != 0 {
			return nil, ctx.Err()
		}
		if pollFDs[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
			continue
		}
		buffer := make([]byte, maxEnrollmentToken+2)
		count, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EINTR) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, io.EOF
		}
		return buffer[:count], nil
	}
}
