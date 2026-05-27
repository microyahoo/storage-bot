package storage

import (
	"context"
	"fmt"
	"strings"
)

// RESTSkill wraps any storage Backend (generic REST or Yanrong) as a queryable
// skill target. It is registered under the storage name and handles cluster-info,
// dir-usage, and health.
type RESTSkill struct {
	name    string
	backend Backend
}

func NewRESTSkill(name string, backend Backend) *RESTSkill {
	return &RESTSkill{name: name, backend: backend}
}

func (s *RESTSkill) StorageName() string { return s.name }

// QueryResult bundles raw output and an optional path that was queried.
type QueryResult struct {
	Output string
	Label  string
}

// Query dispatches based on the action keyword in the user message.
// action: "info" | "health" | "usage <path>"
func (s *RESTSkill) Query(ctx context.Context, action string) (QueryResult, error) {
	action = strings.TrimSpace(strings.ToLower(action))

	switch {
	case action == "health" || action == "健康" || strings.Contains(action, "status"):
		out, err := s.backend.HealthCheck(ctx)
		return QueryResult{Output: out, Label: "健康检查"}, err

	case strings.HasPrefix(action, "usage") || strings.HasPrefix(action, "使用") || strings.HasPrefix(action, "dir"):
		path := strings.TrimPrefix(action, "usage")
		path = strings.TrimPrefix(path, "使用")
		path = strings.TrimPrefix(path, "dir")
		path = strings.TrimSpace(path)
		if path == "" {
			path = "/"
		}
		out, err := s.backend.DirUsage(ctx, path)
		return QueryResult{Output: out, Label: fmt.Sprintf("目录使用(%s)", path)}, err

	default: // "info" or anything else → cluster info
		out, err := s.backend.ClusterInfo(ctx)
		return QueryResult{Output: out, Label: "集群信息"}, err
	}
}
