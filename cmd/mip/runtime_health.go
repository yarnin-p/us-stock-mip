package main

import (
	"sort"
	"sync"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
)

type runtimeHealthRegistry struct {
	mu         sync.RWMutex
	components map[string]dashboard.ComponentHealth
}

func newRuntimeHealthRegistry() *runtimeHealthRegistry {
	return &runtimeHealthRegistry{
		components: make(map[string]dashboard.ComponentHealth),
	}
}

func (registry *runtimeHealthRegistry) Set(
	component dashboard.ComponentHealth,
) {
	if registry == nil || component.Name == "" {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.components[component.Name] = component
}

func (registry *runtimeHealthRegistry) Snapshot() []dashboard.ComponentHealth {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]dashboard.ComponentHealth, 0, len(registry.components))
	for _, component := range registry.components {
		result = append(result, component)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}
