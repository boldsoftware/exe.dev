package execore

import "io"

// pipeStdin creates a reader that copies bytes from r through an io.Pipe.
// When stop is called, the pipe is closed. If the internal goroutine has read
// a byte from r that it can no longer deliver through the pipe, it calls
// pushBack so the byte isn't lost.
//
// This is used to mediate SSH session stdin: without it, the x/crypto/ssh
// internal io.Copy goroutine outlives the session and silently discards
// the next byte read from the underlying reader.
func pipeStdin(r io.Reader, pushBack func([]byte)) (io.Reader, func()) {
	pr, pw := io.Pipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if _, werr := pw.Write(buf[:n]); werr != nil {
					pushBack(buf[:n])
					return
				}
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, func() {
		pw.Close() // reader side sees EOF
		pr.Close() // unblock any pending write
	}
}
