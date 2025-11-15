package exelet

import (
	"context"
	"net"
	"net/url"
	"sync"

	"exe.dev/exelet/services"
	"exe.dev/version"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// Run runs the exelet server.
func (s *Exelet) Run(ctx context.Context) error {
	defer func() {
		s.updateState(api.Server_READY)
	}()

	u, err := url.Parse(s.config.ListenAddress)
	if err != nil {
		return err
	}
	l, err := net.Listen(u.Scheme, u.Host)
	if err != nil {
		return err
	}

	// Log actual listen address (important for tests using port :0)
	actualAddr := u.Scheme + "://" + l.Addr().String()
	s.log.InfoContext(ctx, "listening", "addr", actualAddr)

	doneCh := make(chan bool)
	serviceErrCh := make(chan error)
	wg := &sync.WaitGroup{}
	for _, svc := range s.services {
		wg.Add(1)
		go func(svc services.Service) {
			defer wg.Done()
			s.log.DebugContext(ctx, "starting service", "type", svc.Type())
			if err := svc.Start(ctx); err != nil {
				serviceErrCh <- err
				return
			}
			s.log.InfoContext(ctx, "service started", "type", svc.Type())
		}(svc)
	}

	go func() {
		s.log.DebugContext(ctx, "waiting for services start")
		wg.Wait()
		doneCh <- true
	}()

	select {
	case <-doneCh:
	case err := <-serviceErrCh:
		return err
	}

	s.log.DebugContext(ctx, "starting grpc server", "addr", s.config.ListenAddress)
	go s.grpcServer.Serve(l)

	s.log.InfoContext(ctx, "exelet server ready", "version", version.FullVersion())

	return nil
}
