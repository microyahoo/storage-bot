package inspect

import (
	"context"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelCritical
	LevelUnknown
)

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "OK"
	case LevelWarn:
		return "WARN"
	case LevelCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

func (l Level) Emoji() string {
	switch l {
	case LevelOK:
		return "🟢"
	case LevelWarn:
		return "🟡"
	case LevelCritical:
		return "🔴"
	default:
		return "⚪"
	}
}

type Scope int

const (
	ClusterScope Scope = iota
	NodeScope
)

type Finding struct {
	Item    string            `json:"item"`
	Node    string            `json:"node,omitempty"`
	Level   Level             `json:"level"`
	Summary string            `json:"summary"`
	Metrics map[string]string `json:"metrics,omitempty"`
	Detail  string            `json:"detail,omitempty"`
	Advice  string            `json:"advice,omitempty"`
}

// MaxLevel returns the highest-severity level among findings.
// Severity order: OK < Unknown < Warn < Critical. So a parse failure (Unknown)
// does not mask a healthy cluster, but also does not look worse than a real Warn.
func MaxLevel(findings []Finding) Level {
	rank := map[Level]int{LevelOK: 0, LevelUnknown: 1, LevelWarn: 2, LevelCritical: 3}
	max := LevelOK
	for _, f := range findings {
		if rank[f.Level] > rank[max] {
			max = f.Level
		}
	}
	return max
}

type InspectContext struct {
	Ctx         context.Context
	ClusterName string
	Node        config.SSHNode
	Gateway     *config.SSHNode
	KubeExec    *executor.KubeExecutor
	SSHExec     *executor.SSHExecutor
	Thresholds  config.Thresholds
}

// RunCeph runs `ceph <args...>` via the toolbox pod.
func (ic *InspectContext) RunCeph(args ...string) (string, error) {
	return ic.KubeExec.RunCephCommand(ic.Ctx, args...)
}

// RunOnNode runs cmd on ic.Node, hopping through the gateway when the node is
// not the gateway itself (mirrors skill.Context.RunOnNode).
func (ic *InspectContext) RunOnNode(cmd string) (string, error) {
	if ic.Gateway != nil && executor.HostIP(ic.Node.Host) != executor.HostIP(ic.Gateway.Host) {
		return ic.SSHExec.RunViaGateway(ic.Ctx, *ic.Gateway, ic.Node, cmd)
	}
	return ic.SSHExec.Run(ic.Ctx, ic.Node, cmd)
}

type Inspector interface {
	Name() string
	Description() string
	Scope() Scope
	Inspect(ic *InspectContext) ([]Finding, error)
}
