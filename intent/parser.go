package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	ActionSkill
	ActionListSkills
	ActionRESTStorage // query a non-Ceph REST storage system
	ActionToggleLLM   // enable/disable LLM at runtime
	ActionInspect     // run cluster inspection
	ActionListInspect // list available inspection items
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
	case ActionSkill:
		return "skill"
	case ActionListSkills:
		return "list skills"
	case ActionRESTStorage:
		return "rest storage"
	case ActionToggleLLM:
		return "toggle llm"
	case ActionInspect:
		return "inspect"
	case ActionListInspect:
		return "list inspect"
	default:
		return "unknown"
	}
}

type Action struct {
	Type            ActionType
	ClusterName     string
	ExcludeClusters []string // for "all except X Y Z" or prefix broadcast
	NodeName        string
	SkillName       string
	StorageName     string            // for ActionRESTStorage
	Args            map[string]string // optional skill parameters
	ToggleLLMEnable bool              // for ActionToggleLLM: true=enable, false=disable
	RawMessage      string
}

var clusterNameRe = regexp.MustCompile(`(?i)cluster[- _]?(\S+)`)

// ethNameTokenRe matches a bare NIC interface token (eth0, ens1f0, bond0). Used
// by extractEthName to validate a candidate before treating it as an interface.
var ethNameTokenRe = regexp.MustCompile(`^[A-Za-z0-9_.:\-]+$`)

func Parse(message string, knownClusters []string) Action {
	return ParseWithSkills(message, knownClusters, nil)
}

func ParseWithSkills(message string, knownClusters []string, knownSkills []string) Action {
	return ParseWithAll(message, knownClusters, knownSkills, nil)
}

func ParseWithAll(message string, knownClusters []string, knownSkills []string, knownRESTStorages []string) Action {
	msg := stripMention(message)
	action := Action{RawMessage: msg}
	lower := strings.ToLower(msg)

	// LLM toggle — checked before skill aliases to avoid conflicts.
	if strings.Contains(lower, "llm") {
		action.Type = ActionToggleLLM
		action.ToggleLLMEnable = strings.Contains(lower, "enable") ||
			strings.Contains(lower, "开启") || strings.Contains(lower, "打开") ||
			strings.Contains(lower, "启用") || strings.Contains(lower, "on")
		return action
	}

	// List inspection items — checked BEFORE the inspect run trigger, since
	// "巡检项" ⊃ "巡检" and "list inspect" ⊃ "inspect". A bare "list/列表/有哪些"
	// next to an inspect keyword means "show me the items", not "run a scan".
	if (strings.Contains(lower, "巡检项") || strings.Contains(lower, "体检项")) ||
		((strings.Contains(lower, "list") || strings.Contains(lower, "列表") || strings.Contains(lower, "有哪些") || strings.Contains(lower, "哪些")) &&
			strings.Contains(lower, "inspect")) {
		action.Type = ActionListInspect
		return action
	}

	// Inspection — checked before skill aliases so trigger words 巡检/体检/检查
	// are not swallowed by coarser skill matches (e.g. "检查容量" must run a full
	// inspection, not the capacity skill). Empty ClusterName means "all clusters".
	// "inspect" matches "inspection" too (substring).
	if strings.Contains(lower, "巡检") || strings.Contains(lower, "体检") ||
		strings.Contains(lower, "inspect") {
		action.Type = ActionInspect
		if !strings.Contains(lower, "所有") && !strings.Contains(lower, "全部") && !strings.Contains(lower, "all") {
			action.ClusterName = extractClusterName(lower, knownClusters)
		}
		return action
	}

	// Skill alias table — checked FIRST so multi-word skill commands (e.g. "unset nobackfill all",
	// "list nodes cdn") are not swallowed by the coarser "list clusters" or NodeDiag checks below.
	// Rules:
	//   • unset variants must appear before set variants (substring containment: "unset nobackfill" ⊃ "set nobackfill")
	//   • multi-word / longer aliases before shorter ones within each entry
	type aliasEntry struct {
		skill   string
		aliases []string
	}
	skillAliasTable := []aliasEntry{
		// restart_* first: "重启 mon a" must not be hijacked by mon_status (alias "mon").
		{"restart_mon", []string{"重启mon", "重启 mon", "restart mon", "restart_mon", "重启监视器"}},
		{"restart_mgr", []string{"重启mgr", "重启 mgr", "restart mgr", "restart_mgr"}},
		// unset before set (avoids "unset nobackfill" matching "set nobackfill")
		{"unset_no_backfill", []string{"unset nobackfill", "unset no_backfill", "unset_no_backfill", "取消nobackfill", "恢复迁移", "恢复backfill"}},
		{"unset_noout", []string{"unset noout", "unset_noout", "取消noout"}},
		{"set_no_backfill", []string{"set nobackfill", "set no_backfill", "set_no_backfill", "设置nobackfill", "暂停迁移", "暂停恢复"}},
		{"set_noout", []string{"set noout", "set_noout", "设置noout"}},
		// multi-word node/list aliases before bare "node"
		{"list_nodes", []string{"节点列表", "所有节点", "list nodes", "list_nodes", "list_node", "list node"}},
		{"get_mon_ips", []string{"mon ip", "mon ips", "monitor ip", "mon地址", "monitor地址"}},
		{"get_fsid", []string{"fsid", "集群id", "cluster id"}},
		{"osd_status", []string{"osd状态", "osd"}},
		{"pg_status", []string{"pg"}},
		{"pool_status", []string{"存储池", "pool"}},
		{"capacity", []string{"容量", "capacity", "空间"}},
		{"slow_ops", []string{"慢请求", "慢操作", "slow"}},
		{"crash_info", []string{"crash_info", "crash info", "crash detail", "崩溃详情", "崩溃信息", "最近crash", "最近崩溃"}},
		{"crash", []string{"崩溃", "crash"}},
		{"mon_status", []string{"仲裁", "monitor", "mon"}},
		{"io_stat", []string{"磁盘io", "iostat", "io"}},
		{"kernel_logs", []string{"kernel_logs", "kernel logs", "kernel日志", "kern日志", "内核日志", "kernel"}},
		// nic_down before bond_status/nic_info: "nic down"/"网口down"/"ip link set"
		// all contain the coarser "nic"/"网口"/"ip link" aliases below.
		{"nic_down", []string{"nic_down", "nic down", "网口down", "网口 down", "down网口", "down 网口", "ip link set", "link down", "网口下线", "禁用网口", "关闭网口"}},
		// nic_up after nic_down (symmetric), before nic_info.
		{"nic_up", []string{"nic_up", "nic up", "网口up", "网口 up", "up网口", "up 网口", "网口上线", "启用网口", "恢复网口"}},
		{"bond_status", []string{"bond_status", "bond status", "bond", "网卡聚合", "链路聚合", "link failure"}},
		{"nic_info", []string{"nic_info", "nic info", "nic", "ip link", "网卡", "网卡信息", "网口"}},
		{"optimize_rgw_pg", []string{"optimize rgw", "rgw pg", "rgw pg优化", "upmap rgw", "优化rgw pg", "优化rgw存储池"}},
		{"object_storage", []string{"object_storage", "object storage", "对象存储"}},
	}
	for _, entry := range skillAliasTable {
		for _, alias := range entry.aliases {
			if lower == entry.skill || strings.Contains(lower, alias) {
				action.Type = ActionSkill
				action.SkillName = entry.skill

				// restart_mon/mgr: a message looks like "restart mon a cdn" (with
				// id) or "restart mon cdn" (no id → list candidates). Both the id
				// (a/b/c) and the cluster (cdn) are short tokens, so resolve the
				// CLUSTER first (it's a known set, a reliable anchor), then treat
				// any leftover token between the daemon keyword and the cluster as
				// the id.
				if entry.skill == "restart_mon" || entry.skill == "restart_mgr" {
					action.Args = map[string]string{}
					daemon := strings.TrimPrefix(entry.skill, "restart_") // "mon" / "mgr"

					cluster := extractClusterName(lower, knownClusters)
					action.ClusterName = cluster

					if strings.Contains(lower, "--yes") || strings.Contains(lower, "确认") {
						action.Args["yes"] = "true"
					}

					// Strip daemon keyword, cluster name, --yes, the verb and
					// punctuation; whatever short alnum token remains is the id.
					cleaned := lower
					// fmt.Println(cleaned) // eg: restart mon b cdn --yes
					cleaned = strings.ReplaceAll(cleaned, "--yes", " ")
					// fmt.Println(cleaned) // eg: restart mon b cdn
					if cluster != "" {
						cleaned = strings.ReplaceAll(cleaned, strings.ToLower(cluster), " ")
					}
					// fmt.Println(cleaned) // eg: restart mon b
					for _, w := range []string{"restart", "重启", "确认", daemon} {
						cleaned = strings.ReplaceAll(cleaned, w, " ")
					}
					// fmt.Println(cleaned) // eg: b
					for _, tok := range strings.Fields(cleaned) {
						if len(tok) > 3 || !isAlnum(tok) {
							continue
						}
						// A token that is a substring of some known cluster name is
						// a cluster shorthand (e.g. "cdn" for "cdn-01"), not a daemon
						// id — don't mistake it for the id.
						if action.ClusterName == "" && isClusterFragment(tok, knownClusters) {
							continue
						}
						action.Args["id"] = tok
						break
					}
					return action
				}

				action.ClusterName, action.ExcludeClusters = extractClusterTarget(lower, knownClusters)
				if entry.skill != "list_nodes" {
					action.NodeName = extractNodeName(lower, knownClusters)
				}
				if entry.skill == "optimize_rgw_pg" {
					action.Args = extractSkillArgs(lower, []string{"max"})
				}
				if entry.skill == "kernel_logs" {
					action.Args = extractSkillArgs(lower, []string{"count", "n"})
					if kw := extractKeyword(lower); kw != "" {
						if action.Args == nil {
							action.Args = map[string]string{}
						}
						action.Args["keyword"] = kw
					}
				}
				if entry.skill == "nic_down" || entry.skill == "nic_up" {
					action.Args = map[string]string{}
					if strings.Contains(lower, "--yes") || strings.Contains(lower, "确认") {
						action.Args["yes"] = "true"
					}
					if eth := extractEthName(lower, action.ClusterName, action.NodeName); eth != "" {
						action.Args["eth"] = eth
					}
				}
				return action
			}
		}
	}

	// Check skill names passed in from registry (whole-word match, longest first
	// so "pg_status" beats "pg" when both are registered).
	skills := append([]string(nil), knownSkills...)
	sort.Slice(skills, func(i, j int) bool { return len(skills[i]) > len(skills[j]) })
	for _, sk := range skills {
		if containsWord(lower, strings.ToLower(sk)) {
			action.Type = ActionSkill
			action.SkillName = sk
			action.ClusterName, action.ExcludeClusters = extractClusterTarget(lower, knownClusters)
			action.NodeName = extractNodeName(lower, knownClusters)
			return action
		}
	}

	if strings.Contains(lower, "help") || strings.Contains(lower, "帮助") || strings.Contains(lower, "使用") || msg == "?" {
		action.Type = ActionHelp
		return action
	}

	if (strings.Contains(lower, "list") || strings.Contains(lower, "列表") || strings.Contains(lower, "所有") || strings.Contains(lower, "哪些")) &&
		(strings.Contains(lower, "cluster") || strings.Contains(lower, "集群")) {
		action.Type = ActionListClusters
		return action
	}

	if (strings.Contains(lower, "list") || strings.Contains(lower, "列表") || strings.Contains(lower, "哪些")) &&
		(strings.Contains(lower, "skill") || strings.Contains(lower, "技能") || strings.Contains(lower, "能力")) {
		action.Type = ActionListSkills
		return action
	}

	// Check for REST storage invocation (matched by name as a whole word).
	// Sort by length desc so "yrfs01-sz" beats "yrfs01" when both are configured.
	rest := append([]string(nil), knownRESTStorages...)
	sort.Slice(rest, func(i, j int) bool { return len(rest[i]) > len(rest[j]) })
	for _, storageName := range rest {
		if containsWord(lower, strings.ToLower(storageName)) {
			action.Type = ActionRESTStorage
			action.StorageName = storageName
			action.RawMessage = msg
			return action
		}
	}

	action.ClusterName = extractClusterName(lower, knownClusters)
	action.NodeName = extractNodeName(lower, knownClusters)

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
	if action.Type == ActionHelp || action.Type == ActionListClusters ||
		action.Type == ActionListSkills || action.Type == ActionListInspect ||
		action.Type == ActionToggleLLM {
		return false
	}
	return action.ClusterName == ""
}

func extractClusterName(lower string, knownClusters []string) string {
	// Whole-word match, longest first so "cdn-01-test" beats "cdn-01" when both
	// are configured. Without this, "cdn-01" matches "cdn-01-test capacity"
	// because strings.Contains is substring-based.
	names := append([]string(nil), knownClusters...)
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, name := range names {
		if containsWord(lower, strings.ToLower(name)) {
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

// extractClusterTarget parses the cluster targeting from a broadcast skill message.
// It recognises:
//   - "all" / "所有" / "全部"           → ("all", nil)
//   - "all except A B" / "所有 排除 A B" → ("all", ["A","B"])
//   - a known cluster prefix like "cdn"  → ("cdn*", nil)  [sentinel meaning prefix match]
//   - a single known cluster name        → (name, nil)
func extractClusterTarget(lower string, knownClusters []string) (clusterName string, excludes []string) {
	isAll := false
	for _, kw := range []string{"all", "所有", "全部"} {
		if strings.Contains(lower, kw) {
			isAll = true
			break
		}
	}

	// Parse "except / 排除 / 除了" followed by space-separated cluster tokens.
	for _, sep := range []string{"except", "排除", "除了"} {
		if idx := strings.Index(lower, sep); idx >= 0 {
			rest := strings.TrimSpace(lower[idx+len(sep):])
			for _, tok := range strings.Fields(rest) {
				// Match token against known cluster names (prefix match).
				for _, name := range knownClusters {
					if strings.Contains(strings.ToLower(name), tok) {
						excludes = append(excludes, name)
					}
				}
			}
			break
		}
	}

	if isAll {
		return "all", excludes
	}

	// Check for prefix broadcast: a token that matches multiple clusters.
	// e.g. "set nobackfill cdn" where cdn matches cdn-01, cdn-02, ...
	for _, tok := range strings.Fields(lower) {
		var matched []string
		for _, name := range knownClusters {
			if strings.Contains(strings.ToLower(name), tok) {
				matched = append(matched, name)
			}
		}
		if len(matched) > 1 {
			return tok + "*", nil // sentinel: prefix broadcast
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
	}

	return extractClusterName(lower, knownClusters), nil
}

// extractNodeName picks out a candidate node token from the message.
// It splits on whitespace and returns the first token that is not a known
// cluster name and contains a hyphen or underscore (typical hostname shape).
// The caller (skill execution) does the authoritative match against the real
// node list and reports available nodes if the token matches nothing.
func extractNodeName(lower string, knownClusters []string) string {
	excluded := make(map[string]bool, len(knownClusters))
	for _, c := range knownClusters {
		excluded[strings.ToLower(c)] = true
	}
	for _, tok := range strings.Fields(lower) {
		if excluded[tok] {
			continue
		}
		if strings.Contains(tok, "-") || strings.Contains(tok, "_") {
			return tok
		}
	}
	return ""
}

func stripMention(msg string) string {
	re := regexp.MustCompile(`@_user_\d+\s*`)
	return strings.TrimSpace(re.ReplaceAllString(msg, ""))
}

// containsWord reports whether `name` appears in `s` bounded by non-identifier
// characters on both sides. This prevents "yrfs01" from matching when the user
// actually typed "yrfs01-sz". Identifier characters are [A-Za-z0-9_-.].
func containsWord(s, name string) bool {
	if name == "" {
		return false
	}
	i := 0
	for {
		j := strings.Index(s[i:], name)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(name)
		if (start == 0 || !isNameChar(s[start-1])) && (end == len(s) || !isNameChar(s[end])) {
			return true
		}
		i = start + 1
	}
}

func isNameChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.':
		return true
	}
	return false
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isAlnum reports whether s is non-empty and all ASCII letters/digits.
func isAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// isClusterFragment reports whether tok appears as a substring of any known
// cluster name. Used to avoid mistaking a cluster shorthand (e.g. "cdn" for
// "cdn-01") for a daemon id when parsing restart_mon/mgr.
func isClusterFragment(tok string, knownClusters []string) bool {
	for _, name := range knownClusters {
		if strings.Contains(strings.ToLower(name), tok) {
			return true
		}
	}
	return false
}

// extractSkillArgs parses named numeric parameters from the message.
// Supports "param=N" and "param N" patterns for the given param names.
func extractSkillArgs(lower string, params []string) map[string]string {
	args := make(map[string]string)
	for _, param := range params {
		reEq := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(param) + `\s*=\s*(\d+)\b`)
		if m := reEq.FindStringSubmatch(lower); len(m) > 1 {
			if _, err := strconv.Atoi(m[1]); err == nil {
				args[param] = m[1]
				continue
			}
		}
		reSpace := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(param) + `\s+(\d+)\b`)
		if m := reSpace.FindStringSubmatch(lower); len(m) > 1 {
			if _, err := strconv.Atoi(m[1]); err == nil {
				args[param] = m[1]
			}
		}
	}
	return args
}

// extractKeyword pulls a free-text keyword out of the message for the
// kernel_logs skill. Supports `keyword=X`, `keyword X`, `关键字 X`, `关键字=X`.
// The captured token is alphanumeric (plus `_`/`-`/`.`/`:`) so it is safe to
// inline into the remote shell pipeline.
func extractKeyword(lower string) string {
	patterns := []string{
		`(?i)\bkeyword\s*=\s*([A-Za-z0-9_.:\-]+)`,
		`(?i)\bkeyword\s+([A-Za-z0-9_.:\-]+)`,
		`关键字\s*=\s*([A-Za-z0-9_.:\-]+)`,
		`关键字\s+([A-Za-z0-9_.:\-]+)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		if m := re.FindStringSubmatch(lower); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// extractEthName pulls the NIC interface name (e.g. eth0, ens1f0, enp3s0f1) out
// of a nic_down message. It skips the cluster name, node name, command keywords,
// and flags; the interface token is alnum (plus ._:-), contains at least one
// digit, and is not a hyphenated/underscored hostname. Returns "" if none found.
func extractEthName(lower, clusterName, nodeName string) string {
	skip := map[string]bool{
		"nic": true, "nic_down": true, "down": true, "link": true,
		"set": true, "ip": true, "--yes": true, "yes": true, "确认": true,
		"网口": true, "关闭网口": true, "禁用网口": true, "网口下线": true,
	}
	cl := strings.ToLower(clusterName)
	nd := strings.ToLower(nodeName)
	for _, tok := range strings.Fields(lower) {
		if skip[tok] || tok == cl || tok == nd {
			continue
		}
		// Hostnames in this fleet are hyphenated/underscored; interface names are not.
		if strings.ContainsAny(tok, "-_") {
			continue
		}
		if !ethNameTokenRe.MatchString(tok) {
			continue
		}
		// Require at least one digit so plain words ("link", a cluster fragment)
		// don't get mistaken for an interface; real NICs are eth0/ens1f0/bond0.
		hasDigit := false
		for _, r := range tok {
			if r >= '0' && r <= '9' {
				hasDigit = true
				break
			}
		}
		if hasDigit {
			return tok
		}
	}
	return ""
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
