package bot

import (
	"context"
	"fmt"
	"sort"

	"github.com/microyahoo/storage-bot/storage"
)

// ClusterSummary describes a cluster for the web UI.
type ClusterSummary struct {
	Name        string
	Namespace   string
	Kubeconfig  string
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

// RESTStorageSummary describes a Yanrong (yrfs) storage for the web UI.
type RESTStorageSummary struct {
	Name              string
	Type              string
	BaseURL           string
	PublicUserPrefix  string
	PrivateUserPrefix string
}

// ListRESTStorageSummaries returns sorted REST storage metadata for the web UI.
func (h *Handler) ListRESTStorageSummaries() []RESTStorageSummary {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]RESTStorageSummary, 0, len(h.restStorages))
	for name, sk := range h.restStorages {
		s := RESTStorageSummary{Name: name}
		if meta, ok := sk.Metadata(); ok {
			s.Type = meta.Type
			s.BaseURL = meta.BaseURL
			s.PublicUserPrefix = meta.PublicUserPrefix
			s.PrivateUserPrefix = meta.PrivateUserPrefix
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetRESTStorage looks up a single storage summary by name (case-sensitive).
func (h *Handler) GetRESTStorage(name string) (RESTStorageSummary, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sk, ok := h.restStorages[name]
	if !ok {
		return RESTStorageSummary{}, false
	}
	s := RESTStorageSummary{Name: name}
	if meta, ok2 := sk.Metadata(); ok2 {
		s.Type = meta.Type
		s.BaseURL = meta.BaseURL
		s.PublicUserPrefix = meta.PublicUserPrefix
		s.PrivateUserPrefix = meta.PrivateUserPrefix
	}
	return s, true
}

// restSkill returns the RESTSkill for the named storage, or an error if missing.
func (h *Handler) restSkill(name string) (*storage.RESTSkill, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sk, ok := h.restStorages[name]
	if !ok {
		return nil, fmt.Errorf("storage %q not found", name)
	}
	return sk, nil
}

// GetStorageInfo / GetStorageHealth / GetStorageQuotas / GetStorageUserDir are
// thin web-facing wrappers around RESTSkill so the web handler doesn't import
// the storage package.
func (h *Handler) GetStorageInfo(ctx context.Context, name string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.ClusterInfo(ctx)
}

func (h *Handler) GetStorageHealth(ctx context.Context, name string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.HealthCheck(ctx)
}

func (h *Handler) GetStorageQuotas(ctx context.Context, name string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.ListQuotas(ctx)
}

func (h *Handler) GetStorageUserDir(ctx context.Context, name, user, scope string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.DirUsageForUser(ctx, user, scope)
}

func (h *Handler) GetStorageDirUsage(ctx context.Context, name, path string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.DirUsage(ctx, path)
}

func (h *Handler) GetStorageRecycles(ctx context.Context, name string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.ListRecycles(ctx)
}

func (h *Handler) GetStorageRecycleFiles(ctx context.Context, name, path string) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.ListRecycleFiles(ctx, path, 0)
}

// ClearStorageRecycle clears all files under path from the matching recycle bin.
// With dryRun=true (the default the UI sends on a fresh form load) no destructive
// call is made; the backend still resolves the recycle bin and returns a preview.
// Real deletion (dryRun=false) is restricted by the storage layer to paths under
// public_user_prefix / private_user_prefix.
func (h *Handler) ClearStorageRecycle(ctx context.Context, name, path string, dryRun bool) (string, error) {
	sk, err := h.restSkill(name)
	if err != nil {
		return "", err
	}
	return sk.ClearRecycleFiles(ctx, path, dryRun)
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
