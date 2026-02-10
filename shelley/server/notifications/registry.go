package notifications

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// ChannelFactory creates a Channel from a config map.
// The config map is the raw JSON object from the "notification_channels" array,
// minus the "type" key which was already used for lookup.
type ChannelFactory func(config map[string]any, logger *slog.Logger) (Channel, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]ChannelFactory{}
)

// Register adds a channel factory to the global registry.
// Channel implementations call this in their init() functions.
func Register(typeName string, factory ChannelFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[typeName] = factory
}

// RegisteredTypes returns the names of all registered channel types.
func RegisteredTypes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	types := make([]string, 0, len(registry))
	for t := range registry {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// CreateFromConfig creates a Channel from a config map by looking up
// the "type" field in the registry and calling the corresponding factory.
func CreateFromConfig(config map[string]any, logger *slog.Logger) (Channel, error) {
	typeName, ok := config["type"].(string)
	if !ok || typeName == "" {
		return nil, fmt.Errorf("notification channel config missing \"type\" field")
	}

	registryMu.RLock()
	factory, ok := registry[typeName]
	registryMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown notification channel type: %q", typeName)
	}

	return factory(config, logger)
}
