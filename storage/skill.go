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
// action: "info" | "health" | "usage <path>" | "quotas" | "user <name> [public|private]"
func (s *RESTSkill) Query(ctx context.Context, action string) (QueryResult, error) {
	raw := stripStorageName(action, s.name)
	lower := strings.ToLower(raw)

	switch {
	case lower == "health" || lower == "健康" || strings.Contains(lower, "status"):
		out, err := s.backend.HealthCheck(ctx)
		return QueryResult{Output: out, Label: "健康检查"}, err

	case strings.HasPrefix(lower, "usage") || strings.HasPrefix(lower, "使用") || strings.HasPrefix(lower, "dir"):
		path := stripFirstWord(raw)
		if path == "" {
			path = "/"
		}
		out, err := s.backend.DirUsage(ctx, path)
		return QueryResult{Output: out, Label: fmt.Sprintf("目录使用(%s)", path)}, err

	case strings.Contains(lower, "quota") || strings.Contains(lower, "配额"):
		yb, ok := s.backend.(*YanrongBackend)
		if !ok {
			return QueryResult{}, fmt.Errorf("backend %q does not support quota listing", s.backend.Type())
		}
		out, err := yb.ListQuotas(ctx)
		return QueryResult{Output: out, Label: "配额列表"}, err

	case strings.HasPrefix(lower, "user") || strings.HasPrefix(lower, "用户") ||
		strings.HasPrefix(lower, "private") || strings.HasPrefix(lower, "public") ||
		strings.HasPrefix(lower, "私有") || strings.HasPrefix(lower, "公共"):
		yb, ok := s.backend.(*YanrongBackend)
		if !ok {
			return QueryResult{}, fmt.Errorf("backend %q does not support user-dir lookup", s.backend.Type())
		}
		user, scope := parseUserScope(raw)
		if user == "" {
			return QueryResult{}, fmt.Errorf("user name is required, e.g. 'user aoke private'")
		}
		out, err := yb.DirUsageForUser(ctx, user, scope)
		return QueryResult{Output: out, Label: fmt.Sprintf("用户目录(%s, %s)", user, scope)}, err

	default: // "info" or anything else → cluster info
		out, err := s.backend.ClusterInfo(ctx)
		return QueryResult{Output: out, Label: "集群信息"}, err
	}
}

// stripStorageName drops the storage-name token from the message (case-insensitive)
// so dispatch parsing sees only the action keywords. Also drops a leading "@bot".
func stripStorageName(msg, name string) string {
	out := make([]string, 0, 4)
	for _, f := range strings.Fields(msg) {
		if strings.EqualFold(f, name) {
			continue
		}
		if strings.HasPrefix(f, "@") {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// stripFirstWord removes the leading action keyword and returns the remainder.
// "usage /foo/bar" → "/foo/bar"; "user aoke private" → "aoke private".
func stripFirstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) <= 1 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

// parseUserScope pulls the user name and scope from an action like
// "user aoke private", "private aoke", or "用户 aoke 公共". Scope defaults
// to "private" when not given. Unknown words are treated as the user name.
func parseUserScope(s string) (user, scope string) {
	scope = "private"
	for _, f := range strings.Fields(s) {
		lf := strings.ToLower(f)
		switch lf {
		case "user", "用户", "quota", "配额":
			continue
		case "public", "公共":
			scope = "public"
		case "private", "私有":
			scope = "private"
		default:
			if user == "" {
				user = f
			}
		}
	}
	return user, scope
}
