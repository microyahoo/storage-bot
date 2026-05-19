package skill

import (
	"context"

	"github.com/microyahoo/storage-bot/executor"
)

type Context struct {
	Ctx         context.Context
	ClusterName string
	NodeName    string
	KubeExec    *executor.KubeExecutor
	SSHExec     *executor.SSHExecutor
	Nodes       []SSHTarget
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
