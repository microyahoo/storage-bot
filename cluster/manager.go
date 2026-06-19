package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

type Manager struct {
	mu         sync.RWMutex
	clusters   map[string]*config.ClusterConfig
	nodeCache  map[string][]config.SSHNode       // cluster name → resolved SSH nodes
	kubeCache  map[string]*executor.KubeExecutor // cluster name → cached KubeExecutor
	kubeExecFn func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error)
}

func NewManager(clusters map[string]*config.ClusterConfig) *Manager {
	m := &Manager{
		clusters:  clusters,
		nodeCache: make(map[string][]config.SSHNode),
		kubeCache: make(map[string]*executor.KubeExecutor),
	}
	m.kubeExecFn = func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
		return executor.NewKubeExecutor(cfg.Kubeconfig,
			executor.WithNamespace(cfg.Namespace),
			executor.WithToolboxPodHint(cfg.ToolboxPod),
			executor.WithServerOverride(cfg.ServerOverride),
			executor.WithInsecureSkipTLSVerify(cfg.InsecureSkipTLSVerify),
		)
	}
	return m
}

func (m *Manager) Reload(clusters map[string]*config.ClusterConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters = clusters
	m.nodeCache = make(map[string][]config.SSHNode)
	m.kubeCache = make(map[string]*executor.KubeExecutor)
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

// ListByPrefix returns all cluster names that contain prefix (case-insensitive).
func (m *Manager) ListByPrefix(prefix string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lower := strings.ToLower(prefix)
	var names []string
	for name := range m.clusters {
		if strings.Contains(strings.ToLower(name), lower) {
			names = append(names, name)
		}
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

// ResolveSSHNodes returns SSH-accessible nodes for a cluster.
// If ssh_nodes is set in config, those are returned as-is.
// Otherwise, nodes are auto-discovered from kubernetes and cached.
// All discovered nodes share the gateway_node credentials (passwordless SSH assumed).
func (m *Manager) ResolveSSHNodes(ctx context.Context, clusterName string, cfg *config.ClusterConfig) ([]config.SSHNode, error) {
	// Explicit list overrides auto-discovery
	if len(cfg.SSHNodes) > 0 {
		return cfg.SSHNodes, nil
	}
	if cfg.GatewayNode == nil {
		return nil, nil
	}

	// Check cache first
	m.mu.RLock()
	if nodes, ok := m.nodeCache[clusterName]; ok {
		m.mu.RUnlock()
		return nodes, nil
	}
	m.mu.RUnlock()

	// Auto-discover via k8s API
	ke, err := m.kubeExecFn(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to cluster for node discovery: %w", err)
	}

	discovered, err := ke.DiscoverNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover nodes: %w", err)
	}

	nodes := make([]config.SSHNode, 0, len(discovered))
	for _, n := range discovered {
		nodes = append(nodes, config.SSHNode{
			Name:    n.Name,
			Host:    n.InternalIP + ":22",
			User:    cfg.GatewayNode.User,
			KeyFile: cfg.GatewayNode.KeyFile,
		})
	}

	m.mu.Lock()
	m.nodeCache[clusterName] = nodes
	m.mu.Unlock()

	return nodes, nil
}

// InvalidateNodeCache clears the discovered node list for a cluster (e.g. after scale-up).
func (m *Manager) InvalidateNodeCache(clusterName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodeCache, clusterName)
}

// KubeExecutor returns a cached KubeExecutor for the cluster, creating it on
// first use. Safe for concurrent callers.
func (m *Manager) KubeExecutor(name string, cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
	m.mu.RLock()
	if ke, ok := m.kubeCache[name]; ok {
		m.mu.RUnlock()
		return ke, nil
	}
	m.mu.RUnlock()

	ke, err := m.kubeExecFn(cfg)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.kubeCache[name] = ke
	m.mu.Unlock()
	return ke, nil
}

// SetKubeExecFnForTest swaps the executor factory (test seam).
func (m *Manager) SetKubeExecFnForTest(fn func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error)) {
	m.kubeExecFn = fn
}
