package cluster

import (
	"fmt"
	"strings"
	"sync"

	"github.com/microyahoo/storage-bot/config"
)

type Manager struct {
	mu       sync.RWMutex
	clusters map[string]*config.ClusterConfig
}

func NewManager(clusters map[string]*config.ClusterConfig) *Manager {
	return &Manager{clusters: clusters}
}

func (m *Manager) Reload(clusters map[string]*config.ClusterConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters = clusters
}

func (m *Manager) Get(name string) (*config.ClusterConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.clusters[name]
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", name)
	}
	return c, nil
}

func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clusters))
	for name := range m.clusters {
		names = append(names, name)
	}
	return names
}

func (m *Manager) FindByPrefix(input string) (string, *config.ClusterConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	input = strings.TrimSpace(strings.ToLower(input))

	if c, ok := m.clusters[input]; ok {
		return input, c, nil
	}

	var matches []string
	for name := range m.clusters {
		if strings.Contains(strings.ToLower(name), input) {
			matches = append(matches, name)
		}
	}

	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("no cluster matching %q found", input)
	case 1:
		return matches[0], m.clusters[matches[0]], nil
	default:
		return "", nil, fmt.Errorf("ambiguous cluster name %q, matches: %v", input, matches)
	}
}
