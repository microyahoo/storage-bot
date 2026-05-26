package bot

import (
	"context"
	"fmt"
	"sort"
)

// ClusterSummary describes a cluster for the web UI.
type ClusterSummary struct {
	Name        string
	Namespace   string
	Kubeconfig  string
	ToolboxPod  string
	GatewayHost string
	NodeCount   int // 0 if not yet resolved
}

// NodeInfo is one cluster node row in the web UI.
type NodeInfo struct {
	Name string
	Host string
}

// SkillInfo is one row in the skills table.
type SkillInfo struct {
	Name        string
	Description string
}

// ListClusters returns a sorted summary of every configured cluster.
// Does NOT touch kubernetes — purely from config.
func (h *Handler) ListClusters() []ClusterSummary {
	names := h.clusterMgr.List()
	sort.Strings(names)

	out := make([]ClusterSummary, 0, len(names))
	for _, name := range names {
		cfg, err := h.clusterMgr.Get(name)
		if err != nil {
			continue
		}
		s := ClusterSummary{
			Name:       name,
			Namespace:  cfg.Namespace,
			Kubeconfig: cfg.Kubeconfig,
			ToolboxPod: cfg.ToolboxPod,
		}
		if cfg.GatewayNode != nil {
			s.GatewayHost = cfg.GatewayNode.Host
		}
		out = append(out, s)
	}
	return out
}

// ListSkills returns sorted skill metadata.
func (h *Handler) ListSkills() []SkillInfo {
	skills := h.skills.List()
	out := make([]SkillInfo, 0, len(skills))
	for _, s := range skills {
		out = append(out, SkillInfo{Name: s.Name(), Description: s.Description()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetClusterNodes returns the node list for a cluster (from kubernetes).
// May be slow on first call; subsequent calls hit the manager's cache.
func (h *Handler) GetClusterNodes(ctx context.Context, clusterName string) ([]NodeInfo, error) {
	cfg, err := h.clusterMgr.Get(clusterName)
	if err != nil {
		return nil, err
	}
	nodes, err := h.clusterMgr.ResolveSSHNodes(ctx, clusterName, cfg)
	if err != nil {
		return nil, err
	}
	out := make([]NodeInfo, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodeInfo{Name: n.Name, Host: n.Host})
	}
	return out, nil
}

// GetClusterHealth runs `ceph status / health detail / osd tree / df` and returns
// the combined raw output. Bypasses LLM analysis.
func (h *Handler) GetClusterHealth(ctx context.Context, clusterName string) (string, error) {
	cfg, err := h.clusterMgr.Get(clusterName)
	if err != nil {
		return "", err
	}
	ke, err := h.getKubeExecutor(clusterName, cfg)
	if err != nil {
		return "", fmt.Errorf("connect to cluster %s: %w", clusterName, err)
	}
	return ke.CephHealth(ctx)
}

// RunSkillForWeb invokes a skill against a single cluster and returns the raw
// (or LLM-analyzed, depending on the skill) result. Used by the web UI.
// Returns an error if the skill or cluster is not found.
func (h *Handler) RunSkillForWeb(ctx context.Context, skillName, clusterName, nodeName string, args map[string]string) (string, error) {
	s, ok := h.skills.Get(skillName)
	if !ok {
		return "", fmt.Errorf("skill %q not found", skillName)
	}
	cfg, err := h.clusterMgr.Get(clusterName)
	if err != nil {
		return "", err
	}
	if h.dev.DryRun {
		return h.dryRunReply(clusterName, "skill: "+s.Name(), s.Description()), nil
	}
	return h.runSkillOnCluster(ctx, s, clusterName, cfg, nodeName, args)
}

// LLMEnabled reports the current runtime LLM state (true = enabled).
func (h *Handler) LLMEnabled() bool {
	return !h.llmDisabled.Load() && h.analyzer != nil
}
