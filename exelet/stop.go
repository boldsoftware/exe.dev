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

	wg.Wait()

	return nil
}
