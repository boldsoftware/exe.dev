package hll

import (
	"slices"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector implements prometheus.Collector for HLL metrics.
type Collector struct {
	tracker *Tracker
	events  []string
	mu      sync.RWMutex

	desc *prometheus.Desc
}

// NewCollector creates a Prometheus collector for the given tracker and events.
// The events slice specifies which event types to expose as metrics.
func NewCollector(tracker *Tracker, events []string) *Collector {
	return &Collector{
		tracker: tracker,
		events:  events,
		desc: prometheus.NewDesc(
			"unique_users",
			"Estimated unique users",
			[]string{"event", "period"},
			nil,
		),
	}
}

// SetEvents updates the list of events to track.
// This allows adding new event types at runtime.
func (c *Collector) SetEvents(events []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = events
}

// AddEvent adds a new event type to track if not already present.
func (c *Collector) AddEvent(event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Contains(c.events, event) {
		return
	}
	c.events = append(c.events, event)
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	events := make([]string, len(c.events))
	copy(events, c.events)
	c.mu.RUnlock()

	for _, event := range events {
		dailyCount := c.tracker.GetCurrentDailyCount(event)
		weeklyCount := c.tracker.GetCurrentWeeklyCount(event)

		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(dailyCount),
			event,
			"daily",
		)
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(weeklyCount),
			event,
			"weekly",
		)
	}
}

// Register registers the collector with the given registry.
func (c *Collector) Register(registry *prometheus.Registry) error {
	return registry.Register(c)
}
