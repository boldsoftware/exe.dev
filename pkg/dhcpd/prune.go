package dhcpd

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

func (s *DHCPServer) prune() {
	for range s.pruneTicker.C {
		ctx, cancel := context.WithTimeout(context.Background(), pruneInterval)
		if err := s.pruneReservations(ctx); err != nil {
			s.log.Error("error purging agent registrations", "err", err)
		}
		cancel()
	}
}

func (s *DHCPServer) pruneReservations(ctx context.Context) error {
	doneCh := make(chan struct{})
	errCh := make(chan error)

	go func(ctx context.Context, doneCh chan struct{}, errCh chan error) {
		defer close(doneCh)

		leases, err := s.ds.List()
		if err != nil {
			errCh <- err
			return
		}

		wg := &sync.WaitGroup{}
		for _, l := range leases {
			wg.Add(1)
			// attempt to connect to the IP and release if fails
			go func(l *Lease, wg *sync.WaitGroup) {
				defer wg.Done()

				// don't prune server
				if net.ParseIP(l.IP).Equal(s.serverIP) {
					return
				}

				// check expiration
				expiration := time.Unix(0, int64(l.Expires))
				if time.Now().Before(expiration) {
					return
				}

				isHostUp, err := isUp(l.IP)
				if err != nil {
					errCh <- err
					return
				}

				if !isHostUp {
					s.log.Debug("host not responding; pruning ip",
						"ip", l.IP,
						"mac", l.MACAddress,
					)
					if err := s.ds.Release(l.IP); err != nil {
						errCh <- err
						return
					}
				}
			}(l, wg)
		}

		wg.Wait()
	}(ctx, doneCh, errCh)

	select {
	case <-doneCh:
	case err := <-errCh:
		return err
	case <-ctx.Done():
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return fmt.Errorf("timeout occurred while pruning reservations")
		case context.Canceled:
			return fmt.Errorf("context canceled")
		}
	}

	return nil
}

func isUp(target string) (bool, error) {
	ttl := time.Second * 2
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()

	doneCh := make(chan bool)

	p, err := probing.NewPinger(target)
	if err != nil {
		return false, err
	}
	// if not privileged you will need to set a sysctl (https://github.com/prometheus-community/pro-bing?tab=readme-ov-file#linux)
	p.SetPrivileged(true)
	p.Count = 1
	go func() {
		if err := p.RunWithContext(ctx); err != nil {
			// error reaching host
			doneCh <- false
			return
		}
		// host is up
		doneCh <- true
	}()

	select {
	case <-ctx.Done():
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return false, nil
		case context.Canceled:
			return false, nil
		}
		return false, ctx.Err()
	case result := <-doneCh:
		return result, nil
	}
}
