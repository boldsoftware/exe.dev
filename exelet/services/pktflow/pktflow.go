//go:build linux

package pktflow

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/pktflow/v1"
)

// Service collects per-tap stats and streams them to subscribers via gRPC.
type Service struct {
	api.UnimplementedPktFlowServiceServer
	cfg    *config.ExeletConfig
	log    *slog.Logger
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	subscribers map[int]chan *api.FlowStatsReport
	nextID      int
}

// New creates a new pktflow service.
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	return &Service{
		cfg:         cfg,
		log:         log,
		subscribers: make(map[int]chan *api.FlowStatsReport),
	}, nil
}

func (s *Service) Type() services.Type {
	return services.PktFlowService
}

func (s *Service) Register(_ *services.ServiceContext, server *grpc.Server) error {
	api.RegisterPktFlowServiceServer(server, s)
	return nil
}

func (s *Service) publish(report *api.FlowStatsReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- report:
		default:
		}
	}
}

func (s *Service) subscribe() (int, chan *api.FlowStatsReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	ch := make(chan *api.FlowStatsReport, 16)
	s.subscribers[id] = ch
	return id, ch
}

func (s *Service) unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscribers, id)
}

func (s *Service) StreamFlowStats(_ *api.StreamFlowStatsRequest, stream api.PktFlowService_StreamFlowStatsServer) error {
	id, ch := s.subscribe()
	defer s.unsubscribe(id)

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case report := <-ch:
			if err := stream.Send(report); err != nil {
				return err
			}
		}
	}
}

func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.PktFlowEnabled {
		return nil
	}
	if s.cfg.PktFlowInterval <= 0 {
		s.cfg.PktFlowInterval = config.DefaultPktFlowInterval
	}

	hostID := s.cfg.PktFlowHostID
	if hostID == "" {
		hostID = s.cfg.Name
	}
	if hostID == "" {
		return fmt.Errorf("pktflow host id is required when enabled")
	}

	collector, err := NewCollector(CollectorConfig{
		HostID:         hostID,
		DataDir:        s.cfg.DataDir,
		Interval:       s.cfg.PktFlowInterval,
		MappingRefresh: s.cfg.PktFlowMappingRefresh,
		SampleRate:     s.cfg.PktFlowSampleRate,
		MaxFlows:       s.cfg.PktFlowMaxFlows,
		Publish:        s.publish,
	}, s.log)
	if err != nil {
		return err
	}
	if err := collector.Init(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		collector.Run(runCtx)
	}()

	s.log.InfoContext(ctx, "pktflow collector started",
		"interval", s.cfg.PktFlowInterval)

	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	s.wg.Wait()
	s.log.DebugContext(ctx, "pktflow collector stopped")
	return nil
}
