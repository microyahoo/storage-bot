package skill

import (
	"context"
	"net"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

type Context struct {
	Ctx         context.Context
	ClusterName string
	NodeName    string
	Gateway     *config.SSHNode // nil if bot can reach all nodes directly
	KubeExec    *executor.KubeExecutor
	SSHExec     *executor.SSHExecutor
	Nodes       []SSHTarget
	Args        map[string]string // optional skill parameters (e.g. "max" for upmap)
}

// RunOnNode runs cmd on the given node, using ProxyJump if Gateway is set and
// node is not the gateway itself (compared by IP, ignoring port).
func (sc *Context) RunOnNode(node config.SSHNode, cmd string) (string, error) {
	if sc.Gateway != nil && hostIP(node.Host) != hostIP(sc.Gateway.Host) {
		return sc.SSHExec.RunViaGateway(sc.Ctx, *sc.Gateway, node, cmd)
	}
	return sc.SSHExec.Run(sc.Ctx, node, cmd)
}

func hostIP(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

type SSHTarget struct {
	Name    string
	Host    string
	User    string
	KeyFile string
}

type Skill interface {
	Name() string
	Description() string
	Execute(sc *Context) (string, error)
}

type Registry struct {
	skills map[string]Skill
}

func NewRegistry() *Registry {
	r := &Registry{skills: make(map[string]Skill)}
	r.Register(&OSDStatus{})
	r.Register(&PGStatus{})
	r.Register(&PoolStatus{})
	r.Register(&CapacityCheck{})
	r.Register(&SlowOps{})
	r.Register(&CrashReport{})
	r.Register(&MonStatus{})
	r.Register(&IOStat{})
	r.Register(&ListNodes{})
	r.Register(&GetFSID{})
	r.Register(&GetMonIPs{})
	r.Register(&SetNoBackfillRebalanceRecover{})
	r.Register(&UnsetNoBackfillRebalanceRecover{})
	r.Register(&SetNoout{})
	r.Register(&UnsetNoout{})
	r.Register(&OptimizeRGWBucketsPG{})
	return r
}

func (r *Registry) Register(s Skill) {
	r.skills[s.Name()] = s
}

func (r *Registry) Get(name string) (Skill, bool) {
	s, ok := r.skills[name]
	return s, ok
}

func (r *Registry) List() []Skill {
	out := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out
}
