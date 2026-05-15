package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/microyahoo/storage-bot/analyzer"
)

type ActionType int

const (
	ActionHelp ActionType = iota
	ActionListClusters
	ActionHealth
	ActionLogAnalysis
	ActionNodeDiag
)

func (t ActionType) String() string {
	switch t {
	case ActionHelp:
		return "help"
	case ActionListClusters:
		return "list clusters"
	case ActionHealth:
		return "health"
	case ActionLogAnalysis:
		return "log analysis"
	case ActionNodeDiag:
		return "node diagnostics"
	default:
		return "unknown"
	}
}

type Action struct {
	Type        ActionType
	ClusterName string
	NodeName    string
	RawMessage  string
}

var clusterNameRe = regexp.MustCompile(`(?i)cluster[- _]?(\S+)`)
var nodeNameRe = regexp.MustCompile(`(?i)node[- _]?(\S+)`)

func Parse(message string, knownClusters []string) Action {
	msg := stripMention(message)
	action := Action{RawMessage: msg}
	lower := strings.ToLower(msg)

	if strings.Contains(lower, "help") || strings.Contains(lower, "帮助") || strings.Contains(lower, "使用") || msg == "?" {
		action.Type = ActionHelp
		return action
	}

	if (strings.Contains(lower, "list") || strings.Contains(lower, "列表") || strings.Contains(lower, "所有") || strings.Contains(lower, "哪些")) &&
		(strings.Contains(lower, "cluster") || strings.Contains(lower, "集群")) {
		action.Type = ActionListClusters
		return action
	}

	action.ClusterName = extractClusterName(lower, knownClusters)
	action.NodeName = extractNodeName(lower)

	switch {
	case strings.Contains(lower, "log") || strings.Contains(lower, "日志"):
		action.Type = ActionLogAnalysis
	case action.NodeName != "" || strings.Contains(lower, "磁盘") || strings.Contains(lower, "disk") || strings.Contains(lower, "节点"):
		action.Type = ActionNodeDiag
	default:
		action.Type = ActionHealth
	}

	return action
}

func ParseWithLLM(ctx context.Context, message string, knownClusters []string, llm analyzer.LLMProvider) (Action, error) {
	action := Action{RawMessage: stripMention(message)}

	clustersJSON, _ := json.Marshal(knownClusters)
	systemPrompt := fmt.Sprintf(`You are an intent parser for a Ceph storage cluster management bot.
Given a user message, extract the following information and return ONLY valid JSON (no markdown, no explanation):

{
  "action": "health|logs|node_diag|list|help",
  "cluster": "<cluster name or empty string>",
  "node": "<node name or empty string>"
}

Available clusters: %s

Rules:
- "action" must be one of: health, logs, node_diag, list, help
- If the user asks about cluster status/health/problems/issues, action is "health"
- If the user asks about logs or log analysis, action is "logs"
- If the user asks about a specific node (disk, memory, process), action is "node_diag"
- If the user wants to see all clusters, action is "list"
- If unclear, default to "help"
- "cluster" should match one of the available cluster names. Use fuzzy matching: "01" matches "cluster-01", "集群01" matches "cluster-01"
- "node" is the target node name if mentioned, otherwise empty`, string(clustersJSON))

	resp, err := llm.Chat(ctx, systemPrompt, message)
	if err != nil {
		return action, fmt.Errorf("LLM intent parsing failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	var parsed struct {
		Action  string `json:"action"`
		Cluster string `json:"cluster"`
		Node    string `json:"node"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return action, fmt.Errorf("failed to parse LLM response as JSON: %w, raw: %s", err, resp)
	}

	switch parsed.Action {
	case "health":
		action.Type = ActionHealth
	case "logs":
		action.Type = ActionLogAnalysis
	case "node_diag":
		action.Type = ActionNodeDiag
	case "list":
		action.Type = ActionListClusters
	default:
		action.Type = ActionHelp
	}

	action.ClusterName = parsed.Cluster
	action.NodeName = parsed.Node

	return action, nil
}

func NeedsFallback(action Action) bool {
	if action.Type == ActionHelp || action.Type == ActionListClusters {
		return false
	}
	return action.ClusterName == ""
}

func extractClusterName(lower string, knownClusters []string) string {
	for _, name := range knownClusters {
		if strings.Contains(lower, strings.ToLower(name)) {
			return name
		}
	}

	for _, name := range knownClusters {
		parts := strings.FieldsFunc(name, func(r rune) bool {
			return r == '-' || r == '_'
		})
		for _, part := range parts {
			if len(part) > 0 && isNumeric(part) && strings.Contains(lower, part) {
				return name
			}
		}
	}

	if m := clusterNameRe.FindStringSubmatch(lower); len(m) > 1 {
		return m[1]
	}

	return ""
}

func extractNodeName(lower string) string {
	if m := nodeNameRe.FindStringSubmatch(lower); len(m) > 1 {
		return m[1]
	}
	return ""
}

func stripMention(msg string) string {
	re := regexp.MustCompile(`@_user_\d+\s*`)
	return strings.TrimSpace(re.ReplaceAllString(msg, ""))
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return s
}
