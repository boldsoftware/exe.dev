package testinfra

import (
	"bufio"
	"net"
	"sync"
	"testing"
)

func TestTCPProxy(t *testing.T) {
	p, err := NewTCPProxy("test")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Serve(t.Context())
	}()

	c, err := net.DialTCP("tcp", nil, p.Address())
	if err != nil {
		t.Fatal(err)
	}
	const msg = "hello\n"
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}

	ln, err := net.ListenTCP("tcp", nil)
	if err != nil {
		t.Fatal(err)
	}

	var readWG sync.WaitGroup
	readWG.Add(1)
	go func() {
		defer readWG.Done()

		rc, err := ln.AcceptTCP()
		if err != nil {
			t.Error(err)
			return
		}

		rcb := bufio.NewReader(rc)
		s, err := rcb.ReadString('\n')
		if err != nil {
			t.Error(err)
			return
		}

		if s != msg {
			t.Errorf("read %q want %q", s, msg)
		}
	}()

	p.SetDestPort(ln.Addr().(*net.TCPAddr).Port)

	readWG.Wait()

	p.Close()

	wg.Wait()
}
