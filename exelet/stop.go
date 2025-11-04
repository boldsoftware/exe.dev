package exelet

import (
	"context"
	"sync"

	"exe.dev/exelet/services"
)

func (s *Exelet) Stop(ctx context.Context) error {
	s.log.Debug("stopping server")

	// stop services
	wg := &sync.WaitGroup{}
	for _, svc := range s.services {
		wg.Add(1)
		go func(svc services.Service) {
			defer wg.Done()
			s.log.Debug("stopping service", "type", svc.Type())
			if err := svc.Stop(ctx); err != nil {
				s.log.Error("error stopping service", "type", svc.Type(), "err", err)
			}
		}(svc)
	}

	s.log.Debug("waiting for services to shutdown")

	wg.Wait()

	return nil
}
