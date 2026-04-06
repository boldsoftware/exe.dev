package exelet

import (
	"context"
	"sync"

	"exe.dev/exelet/services"
)

func (s *Exelet) Stop(ctx context.Context) error {
	s.log.DebugContext(ctx, "stopping server")

	// stop services
	wg := &sync.WaitGroup{}
	for _, svc := range s.services {
		wg.Add(1)
		go func(svc services.Service) {
			defer wg.Done()
			s.log.DebugContext(ctx, "stopping service", "type", svc.Type())
			if err := svc.Stop(ctx); err != nil {
				s.log.ErrorContext(ctx, "error stopping service", "type", svc.Type(), "err", err)
			}
		}(svc)
	}

	s.log.DebugContext(ctx, "waiting for services to shutdown")

	// Respect the shutdown context deadline. If services don't finish
	// in time, return the context error so the caller isn't blocked
	// indefinitely (systemd's TimeoutStopSec is the final backstop).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	var ctxErr error
	select {
	case <-done:
	case <-ctx.Done():
		s.log.WarnContext(ctx, "shutdown context expired, some services may still be stopping")
		ctxErr = ctx.Err()
	}

	// Stop the gRPC server after services have drained so that clients
	// can still poll migration status during the drain window. Once
	// services are done, send GOAWAY so clients get a clean closure
	// instead of a connection reset on process exit.
	if s.grpcServer != nil {
		s.log.DebugContext(ctx, "stopping gRPC server")
		s.grpcServer.GracefulStop()
	}

	return ctxErr
}
