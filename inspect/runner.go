package inspect

import (
	"context"
	"sync"
	"time"

	"github.com/microyahoo/storage-bot/analyzer"
	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

// ClusterProvider is the slice of cluster.Manager that Runner needs.
type ClusterProvider interface {
	FindByPrefix(input string) (string, *config.ClusterConfig, error)
	KubeExecutor(name string, cfg *config.ClusterConfig) (*executor.KubeExecutor, error)
	ResolveSSHNodes(ctx context.Context, name string, cfg *config.ClusterConfig) ([]config.SSHNode, error)
}

type Runner struct {
	registry   *Registry
	clusters   ClusterProvider
	sshExec    *executor.SSHExecutor
	analyzer   *analyzer.Analyzer // 可空：LLM 关闭时为 nil
	thresholds config.Thresholds
	llmSummary bool
	store      *Store // 可空
	maxNodePar int    // 节点并发上限，0 → 8
}

func NewRunner(reg *Registry, clusters ClusterProvider, ssh *executor.SSHExecutor,
	az *analyzer.Analyzer, thresholds config.Thresholds, llmSummary bool, store *Store) *Runner {
	return &Runner{
		registry: reg, clusters: clusters, sshExec: ssh, analyzer: az,
		thresholds: thresholds, llmSummary: llmSummary, store: store, maxNodePar: 8,
	}
}

// InspectItem is a one-line description of a registered inspector, for help/list
// surfaces. ClusterScope items run once per cluster; NodeScope items run per node.
type InspectItem struct {
	Name        string
	Description string
	Scope       Scope
}

// Items returns all registered inspectors as InspectItem, cluster-scope first.
func (r *Runner) Items() []InspectItem {
	var out []InspectItem
	for _, s := range []Scope{ClusterScope, NodeScope} {
		for _, in := range r.registry.ByScope(s) {
			out = append(out, InspectItem{Name: in.Name(), Description: in.Description(), Scope: s})
		}
	}
	return out
}

// Run resolves the cluster, runs all inspectors, persists and returns the report.
func (r *Runner) Run(ctx context.Context, clusterInput string) (*Report, error) {
	name, cfg, err := r.clusters.FindByPrefix(clusterInput)
	if err != nil {
		return nil, err
	}
	ke, err := r.clusters.KubeExecutor(name, cfg)
	if err != nil {
		return nil, err
	}
	nodes, err := r.clusters.ResolveSSHNodes(ctx, name, cfg)
	if err != nil {
		return nil, err
	}

	// 取上一次报告用于 bond link failure 增量比对（此刻本次尚未 Save）。
	prev := r.latestReport(name)

	start := time.Now()
	rep := r.runWith(ctx, name, ke, cfg.GatewayNode, nodes, r.thresholds, start)

	// 增量修正必须在 Finalize（Overall 依赖 level）与 summarize（降级项不喂 LLM）之前。
	applyBondDelta(rep, prev)
	rep.Finalize()
	rep.Duration = time.Since(start)

	if r.llmSummary && r.analyzer != nil {
		if s, err := r.summarize(ctx, rep); err == nil {
			rep.LLMSummary = s
		}
	}
	if r.store != nil {
		_ = r.store.Save(rep)
	}
	return rep, nil
}

// latestReport loads the most recent stored report for the cluster, or nil when
// there is no store or no history yet (first run). Used as the bond link-failure
// delta baseline.
func (r *Runner) latestReport(cluster string) *Report {
	if r.store == nil {
		return nil
	}
	names, err := r.store.List(cluster)
	if err != nil || len(names) == 0 {
		return nil
	}
	rep, err := r.store.Load(cluster, names[0])
	if err != nil {
		return nil
	}
	return rep
}

// runWith is the testable core: no network resolution, time injected by caller.
func (r *Runner) runWith(ctx context.Context, name string, ke *executor.KubeExecutor,
	gateway *config.SSHNode, nodes []config.SSHNode, th config.Thresholds, start time.Time) *Report {
	rep := &Report{Cluster: name, StartedAt: start}

	// ClusterScope：串行跑一次
	for _, in := range r.registry.ByScope(ClusterScope) {
		ic := &InspectContext{Ctx: ctx, ClusterName: name, KubeExec: ke, SSHExec: r.sshExec, Thresholds: th}
		if fs, err := in.Inspect(ic); err == nil {
			rep.Findings = append(rep.Findings, fs...)
		}
	}

	// NodeScope：每节点并发，带上限；单节点失败不影响其他
	nodeInspectors := r.registry.ByScope(NodeScope)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, max(1, r.maxNodePar))
	)
	for _, node := range nodes {
		wg.Add(1)
		go func(node config.SSHNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var local []Finding
			for _, in := range nodeInspectors {
				ic := &InspectContext{Ctx: ctx, ClusterName: name, Node: node,
					Gateway: gateway, KubeExec: ke, SSHExec: r.sshExec, Thresholds: th}
				if fs, err := in.Inspect(ic); err == nil {
					for _, f := range fs {
						if f.Node == "" {
							f.Node = node.Name
						}
						if f.NodeIP == "" {
							f.NodeIP = executor.HostIP(node.Host)
						}
						local = append(local, f)
					}
				}
			}
			mu.Lock()
			rep.Findings = append(rep.Findings, local...)
			mu.Unlock()
		}(node)
	}
	wg.Wait()

	// Finalize here so callers using runWith directly (tests) get Overall set;
	// Run re-Finalizes after applyBondDelta adjusts levels.
	rep.Finalize()
	return rep
}

// summarize feeds the abnormal findings (structured, not raw output) to the LLM.
func (r *Runner) summarize(ctx context.Context, rep *Report) (string, error) {
	var b []byte
	for _, f := range rep.Abnormal() {
		b = append(b, []byte(f.Level.String()+" "+itemLabel(f)+": "+f.Summary+"\n")...)
	}
	if len(b) == 0 {
		return "", nil
	}
	return r.analyzer.Analyze(ctx, rep.Cluster, string(b))
}
