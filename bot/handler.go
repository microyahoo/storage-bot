package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/microyahoo/storage-bot/analyzer"
	"github.com/microyahoo/storage-bot/card"
	"github.com/microyahoo/storage-bot/cluster"
	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
	"github.com/microyahoo/storage-bot/intent"
	"github.com/microyahoo/storage-bot/security"
	"github.com/microyahoo/storage-bot/skill"
	"github.com/microyahoo/storage-bot/storage"
)

type Handler struct {
	feishuClient *lark.Client
	clusterMgr   *cluster.Manager
	sshExec      *executor.SSHExecutor
	analyzer     *analyzer.Analyzer
	llm          analyzer.LLMProvider
	skills       *skill.Registry
	restStorages map[string]*storage.RESTSkill
	audit        *security.AuditLog
	dev          config.DevConfig
	kubeCache    map[string]*executor.KubeExecutor
	mu           sync.Mutex
	llmDisabled  atomic.Bool // runtime toggle; initialized from dev.DisableLLM
}

type HandlerOption func(*Handler)

func WithAnalyzer(az *analyzer.Analyzer) HandlerOption {
	return func(h *Handler) { h.analyzer = az }
}

func WithLLM(llm analyzer.LLMProvider) HandlerOption {
	return func(h *Handler) { h.llm = llm }
}

func WithSkills(skills *skill.Registry) HandlerOption {
	return func(h *Handler) { h.skills = skills }
}

func WithAudit(audit *security.AuditLog) HandlerOption {
	return func(h *Handler) { h.audit = audit }
}

func WithDev(dev config.DevConfig) HandlerOption {
	return func(h *Handler) { h.dev = dev }
}

func NewHandler(feishuClient *lark.Client, mgr *cluster.Manager, sshExec *executor.SSHExecutor, opts ...HandlerOption) *Handler {
	h := &Handler{
		feishuClient: feishuClient,
		clusterMgr:   mgr,
		sshExec:      sshExec,
		restStorages: make(map[string]*storage.RESTSkill),
		kubeCache:    make(map[string]*executor.KubeExecutor),
	}
	for _, opt := range opts {
		opt(h)
	}
	h.llmDisabled.Store(h.dev.DisableLLM)
	return h
}

func (h *Handler) AddRESTStorage(name string, s *storage.RESTSkill) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restStorages[name] = s
}

// ReplaceRESTStorages atomically swaps the entire REST storage map. Used by
// config hot-reload so adds/removes/edits to rest_storages take effect without
// a restart.
func (h *Handler) ReplaceRESTStorages(m map[string]*storage.RESTSkill) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restStorages = m
}

func (h *Handler) ListRESTStorages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.restStorages))
	for name := range h.restStorages {
		names = append(names, name)
	}
	return names
}

func (h *Handler) InvalidateKubeCache() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kubeCache = make(map[string]*executor.KubeExecutor)
}

func (h *Handler) HandleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	msg := event.Event.Message
	if msg == nil || msg.Content == nil {
		return nil
	}

	text := extractText(*msg.Content)
	if text == "" {
		return nil
	}

	// Ignore @all messages in group chats — only respond to direct @bot mentions.
	if msg.ChatType != nil && *msg.ChatType == "group" {
		if !isBotMentioned(msg.Mentions) {
			slog.Info("ignoring @all message in group", "text", text)
			return nil
		}
	}

	userID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil && event.Event.Sender.SenderId.OpenId != nil {
		userID = *event.Event.Sender.SenderId.OpenId
	}

	slog.Info("received message", "text", text, "chat_type", *msg.ChatType, "user", userID)

	sanitized := security.SanitizeForLLM(text)

	knownSkills := make([]string, 0)
	for _, s := range h.skills.List() {
		knownSkills = append(knownSkills, s.Name())
	}
	action := intent.ParseWithAll(sanitized, h.clusterMgr.List(), knownSkills, h.ListRESTStorages())

	if intent.NeedsFallback(action) && !h.llmDisabled.Load() && h.llm != nil {
		slog.Info("regex parse incomplete, trying LLM fallback", "raw", sanitized)
		llmAction, err := intent.ParseWithLLM(ctx, sanitized, h.clusterMgr.List(), h.llm)
		if err != nil {
			slog.Warn("LLM intent parsing failed, using regex result", "error", err)
		} else {
			action = llmAction
		}
	} else if intent.NeedsFallback(action) && h.llmDisabled.Load() {
		slog.Info("[dev] LLM fallback disabled, using regex result as-is", "raw", sanitized)
	}

	slog.Info("parsed intent",
		"type", action.Type,
		"skill", action.SkillName,
		"cluster", action.ClusterName,
		"node", action.NodeName,
		"storage", action.StorageName,
	)

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var (
		reply string
		err   error
	)

	switch action.Type {
	case intent.ActionHelp:
		reply = h.helpMessage()
	case intent.ActionListClusters:
		reply = h.listClusters()
	case intent.ActionListSkills:
		reply = h.listSkills()
	case intent.ActionToggleLLM:
		reply = h.toggleLLM(action.ToggleLLMEnable)
	case intent.ActionSkill:
		reply, err = h.handleSkill(ctx, action)
	case intent.ActionHealth:
		reply, err = h.handleHealth(ctx, action)
	case intent.ActionLogAnalysis:
		reply, err = h.handleLogAnalysis(ctx, action)
	case intent.ActionNodeDiag:
		reply, err = h.handleNodeDiag(ctx, action)
	case intent.ActionRESTStorage:
		reply, err = h.handleRESTStorage(ctx, action)
	}

	status := "ok"
	if err != nil {
		status = "error: " + err.Error()
		reply = fmt.Sprintf("```\n%v\n```", err)
	}

	if h.audit != nil {
		h.audit.Record(userID, action.ClusterName, action.Type.String(), text, status)
	}

	c := cardForAction(action, reply, err)
	return h.replyCard(ctx, *msg.MessageId, c)
}

func (h *Handler) handleHealth(ctx context.Context, action intent.Action) (string, error) {
	if action.ClusterName == "" {
		return "📝 请指定集群名称，例如：`check cluster-01 health`\n\n" + h.listClusters(), nil
	}

	clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
	if err != nil {
		return "", err
	}

	if h.dev.DryRun {
		return h.dryRunReply(clusterName, "health", "ceph status / health detail / osd tree / df"), nil
	}

	kubeExec, err := h.getKubeExecutor(clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("connect to cluster %s: %w", clusterName, err)
	}

	diagnostics, err := kubeExec.CephHealth(ctx)
	if err != nil {
		return "", fmt.Errorf("get ceph health: %w", err)
	}

	return h.analyzeOrEcho(ctx, clusterName, "健康检查", diagnostics), nil
}

func (h *Handler) handleLogAnalysis(ctx context.Context, action intent.Action) (string, error) {
	if action.ClusterName == "" {
		return "📝 请指定集群名称，例如：`analyze logs cluster-01`\n\n" + h.listClusters(), nil
	}

	clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
	if err != nil {
		return "", err
	}

	allSSHNodes, err := h.clusterMgr.ResolveSSHNodes(ctx, clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("resolve nodes for %s: %w", clusterName, err)
	}
	if len(allSSHNodes) == 0 {
		return fmt.Sprintf("⚠️ 集群 `%s` 没有配置 SSH 节点，请配置 `gateway_node` 或 `ssh_nodes`", clusterName), nil
	}

	var allLogs []string
	targetNodes := allSSHNodes
	if action.NodeName != "" {
		targetNodes = filterNodes(allSSHNodes, action.NodeName)
		if len(targetNodes) == 0 {
			return fmt.Sprintf("🚫 集群 `%s` 中未找到节点 `%s`", clusterName, action.NodeName), nil
		}
	}

	if h.dev.DryRun {
		return h.dryRunReply(clusterName, "log analysis", "ssh to nodes, read /var/log/messages, /var/lib/rook/rook-ceph/log/*"), nil
	}

	for _, node := range targetNodes {
		var (
			logs string
			err  error
		)
		if gw := clusterCfg.GatewayNode; gw != nil && !isGateway(node.Host, gw.Host) {
			logs, err = h.sshExec.CollectLogsViaGateway(ctx, *gw, node)
		} else {
			logs, err = h.sshExec.CollectLogs(ctx, node)
		}
		if err != nil {
			allLogs = append(allLogs, fmt.Sprintf("=== Node %s ERROR ===\n%v", node.Name, err))
		} else {
			allLogs = append(allLogs, fmt.Sprintf("=== Node %s Logs ===\n%s", node.Name, logs))
		}
	}

	diagnostics := strings.Join(allLogs, "\n\n")
	return h.analyzeOrEcho(ctx, clusterName, "日志分析", diagnostics), nil
}

func (h *Handler) handleNodeDiag(ctx context.Context, action intent.Action) (string, error) {
	if action.ClusterName == "" {
		return "📝 请指定集群名称，例如：`check node-1 cluster-01`\n\n" + h.listClusters(), nil
	}

	clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
	if err != nil {
		return "", err
	}

	if action.NodeName == "" {
		return "📝 请指定节点名称，例如：`check node-1 cluster-01`", nil
	}

	allSSHNodes, err := h.clusterMgr.ResolveSSHNodes(ctx, clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("resolve nodes for %s: %w", clusterName, err)
	}

	nodes := filterNodes(allSSHNodes, action.NodeName)
	if len(nodes) == 0 {
		return fmt.Sprintf("🚫 集群 `%s` 中未找到节点 `%s`", clusterName, action.NodeName), nil
	}

	node := nodes[0]
	if h.dev.DryRun {
		return h.dryRunReply(clusterName, "node diag on "+node.Name, "ssh: dmesg, df, free, ps, ip, uptime"), nil
	}

	var (
		diagnostics string
		diagErr     error
	)
	if gw := clusterCfg.GatewayNode; gw != nil && !isGateway(node.Host, gw.Host) {
		diagnostics, diagErr = h.sshExec.NodeDiagnosticsViaGateway(ctx, *gw, node)
	} else {
		diagnostics, diagErr = h.sshExec.NodeDiagnostics(ctx, node)
	}
	if diagErr != nil {
		return "", fmt.Errorf("node diagnostics for %s: %w", node.Name, diagErr)
	}

	return h.analyzeOrEcho(ctx, clusterName, fmt.Sprintf("节点 %s 诊断", node.Name), diagnostics), nil
}

func (h *Handler) helpMessage() string {
	return "支持中英文自然语言输入。以下命令均可 `@bot` 直接发送：\n\n" +
		"**🗂 集群与列表**\n" +
		"- `list clusters` / `列表集群` / `有哪些集群`\n" +
		"- `list skills` / `有哪些技能`\n\n" +
		"**🩺 健康 & 诊断**\n" +
		"- 健康检查：`check cluster-01` / `看看01集群怎么了`\n" +
		"- 日志分析：`analyze logs cluster-01`\n" +
		"- 节点诊断：`check node-1 cluster-01`\n\n" +
		"**⚙️ Skill 执行**\n" +
		"- 单集群：`osd cluster-01` / `cluster-02 容量`\n" +
		"- 批量（仅 set/unset nobackfill/noout）：\n" +
		"  - 前缀匹配：`set nobackfill cdn`\n" +
		"  - 全量：`set nobackfill all`\n" +
		"  - 全量排除：`set nobackfill all except cdn-test cdn-staging`\n\n" +
		"**💽 磁盘 IO**\n" +
		"- 所有节点：`iostat cdn`\n" +
		"- 指定节点：`iostat cdn bd-cdn-node02`\n\n" +
		"**📈 RGW PG 优化**\n" +
		"- `optimize rgw cluster-01`（默认 max=100）\n" +
		"- `optimize rgw cluster-01 max=50`\n\n" +
		"**🤖 LLM 开关**\n" +
		"- `enable llm` / `开启llm`\n" +
		"- `disable llm` / `关闭llm`\n\n" +
		"**📦 焱融(yrfs) 存储**（按 `rest_storages` 名称路由，如 `yrfs01`）\n" +
		"- 集群信息：`yrfs01` / `yrfs01 info`\n" +
		"- 健康状态：`yrfs01 health`\n" +
		"- 配额列表：`yrfs01 quotas`\n" +
		"- 精确路径：`yrfs01 usage /drtraining/user/liangzheng`\n" +
		"- 用户目录：`yrfs01 user liangzheng`（默认 private）\n" +
		"  - 公共目录：`yrfs01 user liangzheng public`\n\n" +
		"**💡 示例**\n" +
		"```\n" +
		"@bot 帮我看看cluster-01的状态\n" +
		"@bot 分析一下cluster-02的日志\n" +
		"@bot iostat cdn bd-cdn-node02\n" +
		"@bot set nobackfill all except cdn-test\n" +
		"@bot optimize rgw cluster-01 max=100\n" +
		"@bot yrfs01 user liangzheng private\n" +
		"@bot yrfs01 quotas\n" +
		"```"
}

func (h *Handler) listClusters() string {
	cephs := h.ListClusters()
	rests := h.ListRESTStorageSummaries()

	if len(cephs) == 0 && len(rests) == 0 {
		return "🚫 当前没有配置任何集群或存储"
	}

	var sb strings.Builder

	if len(cephs) > 0 {
		sb.WriteString(fmt.Sprintf("🛰 **Ceph 集群** · %d 套\n", len(cephs)))
		for _, c := range cephs {
			sb.WriteString(fmt.Sprintf("　🔹 `%s`\n", c.Name))
		}
	}

	if len(rests) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("📦 **焱融(yrfs) 存储** · %d 套\n", len(rests)))
		for _, r := range rests {
			sb.WriteString(fmt.Sprintf("　🔸 `%s`\n", r.Name))
		}
	}

	return sb.String()
}

func (h *Handler) listSkills() string {
	skills := h.skills.List()
	if len(skills) == 0 {
		return "🚫 当前没有注册任何 Skill"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🧰 共 **%d** 个 Skill\n", len(skills)))
	for _, s := range skills {
		sb.WriteString(fmt.Sprintf("　🔧 `%s` — %s\n", s.Name(), s.Description()))
	}
	return sb.String()
}

func (h *Handler) toggleLLM(enable bool) string {
	h.llmDisabled.Store(!enable)
	if enable {
		return "✅ LLM 已**开启** — 将使用 AI 进行意图解析和结果分析"
	}
	return "🔕 LLM 已**关闭** — 仅使用规则解析，不调用 AI"
}

func (h *Handler) handleSkill(ctx context.Context, action intent.Action) (string, error) {
	s, ok := h.skills.Get(action.SkillName)
	if !ok {
		return fmt.Sprintf("🚫 未找到 Skill `%s`\n\n%s", action.SkillName, h.listSkills()), nil
	}

	// Resolve target cluster list.
	var targetClusters []string
	switch {
	case action.ClusterName == "all":
		if !isBroadcastAllowed(action.SkillName) {
			return fmt.Sprintf("⚠️ Skill `%s` 不支持批量执行，请指定集群名称", action.SkillName), nil
		}
		targetClusters = h.clusterMgr.List()
	case strings.HasSuffix(action.ClusterName, "*"):
		if !isBroadcastAllowed(action.SkillName) {
			return fmt.Sprintf("⚠️ Skill `%s` 不支持批量执行，请指定集群名称", action.SkillName), nil
		}
		prefix := strings.TrimSuffix(action.ClusterName, "*")
		targetClusters = h.clusterMgr.ListByPrefix(prefix)
		if len(targetClusters) == 0 {
			return fmt.Sprintf("🚫 没有匹配前缀 `%s` 的集群\n\n%s", prefix, h.listClusters()), nil
		}
	case action.ClusterName == "":
		return fmt.Sprintf("📝 请指定集群名称来执行 Skill `%s`，或输入 `all` 对所有集群执行\n\n%s", action.SkillName, h.listClusters()), nil
	default:
		// Single cluster.
		clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
		if err != nil {
			return "", err
		}
		if h.dev.DryRun {
			return h.dryRunReply(clusterName, "skill: "+s.Name(), s.Description()), nil
		}
		return h.runSkillOnCluster(ctx, s, clusterName, clusterCfg, action.NodeName, action.Args)
	}

	// Apply exclusions.
	if len(action.ExcludeClusters) > 0 {
		excluded := make(map[string]bool, len(action.ExcludeClusters))
		for _, e := range action.ExcludeClusters {
			excluded[e] = true
		}
		filtered := targetClusters[:0]
		for _, c := range targetClusters {
			if !excluded[c] {
				filtered = append(filtered, c)
			}
		}
		targetClusters = filtered
	}

	if len(targetClusters) == 0 {
		return "🚫 排除后没有剩余目标集群", nil
	}

	return h.handleSkillOnClusters(ctx, s, targetClusters, action.ExcludeClusters)
}

// isBroadcastAllowed returns true for skills safe to run on all clusters at once.
// Only flag set/unset ops are allowed — read-only skills on 30 clusters would be too noisy.
var broadcastSkills = map[string]bool{
	"set_no_backfill":   true,
	"unset_no_backfill": true,
	"set_noout":         true,
	"unset_noout":       true,
}

func isBroadcastAllowed(skillName string) bool {
	return broadcastSkills[skillName]
}

func (h *Handler) handleSkillOnClusters(ctx context.Context, s skill.Skill, clusters []string, excludes []string) (string, error) {
	var results []string
	for _, clusterName := range clusters {
		clusterCfg, err := h.clusterMgr.Get(clusterName)
		if err != nil {
			results = append(results, fmt.Sprintf("**❌ %s**：获取集群配置失败：%v", clusterName, err))
			continue
		}
		if h.dev.DryRun {
			results = append(results, h.dryRunReply(clusterName, "skill: "+s.Name(), s.Description()))
			continue
		}
		output, err := h.runSkillOnCluster(ctx, s, clusterName, clusterCfg, "", nil)
		if err != nil {
			results = append(results, fmt.Sprintf("**❌ %s**：执行失败：%v", clusterName, err))
		} else {
			results = append(results, fmt.Sprintf("**✅ %s**\n%s", clusterName, output))
		}
	}
	excludeNote := ""
	if len(excludes) > 0 {
		excludeNote = fmt.Sprintf("（已排除：`%s`）", strings.Join(excludes, "`、`"))
	}
	return fmt.Sprintf("🚀 批量执行 **%s** · 共 %d 套集群%s\n\n%s",
		s.Description(), len(clusters), excludeNote, strings.Join(results, "\n\n---\n\n")), nil
}

func (h *Handler) runSkillOnCluster(ctx context.Context, s skill.Skill, clusterName string, clusterCfg *config.ClusterConfig, nodeName string, args map[string]string) (string, error) {
	kubeExec, err := h.getKubeExecutor(clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("connect to cluster %s: %w", clusterName, err)
	}

	allSSHNodes, err := h.clusterMgr.ResolveSSHNodes(ctx, clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("resolve nodes for %s: %w", clusterName, err)
	}

	nodes := make([]skill.SSHTarget, 0, len(allSSHNodes))
	for _, n := range allSSHNodes {
		nodes = append(nodes, skill.SSHTarget{
			Name:    n.Name,
			Host:    n.Host,
			User:    n.User,
			KeyFile: n.KeyFile,
		})
	}

	sc := &skill.Context{
		Ctx:         ctx,
		ClusterName: clusterName,
		NodeName:    nodeName,
		Gateway:     clusterCfg.GatewayNode,
		KubeExec:    kubeExec,
		SSHExec:     h.sshExec,
		Nodes:       nodes,
		Args:        args,
	}

	output, err := s.Execute(sc)
	if err != nil {
		return "", fmt.Errorf("skill %s on %s failed: %w", s.Name(), clusterName, err)
	}

	// list_nodes / get_fsid / get_mon_ips return plain text, no LLM analysis needed
	if !needsAnalysis(s.Name()) {
		return output, nil
	}

	return h.analyzeOrEcho(ctx, clusterName, s.Description(), output), nil
}

// needsAnalysis returns false for skills whose output is already human-readable
// and doesn't benefit from LLM summarization.
var noAnalysisSkills = map[string]bool{
	"list_nodes":        true,
	"get_fsid":          true,
	"get_mon_ips":       true,
	"set_no_backfill":   true,
	"unset_no_backfill": true,
	"set_noout":         true,
	"unset_noout":       true,
	"optimize_rgw_pg":   true,
}

func needsAnalysis(skillName string) bool {
	return !noAnalysisSkills[skillName]
}

func (h *Handler) handleRESTStorage(ctx context.Context, action intent.Action) (string, error) {
	h.mu.Lock()
	rs, ok := h.restStorages[action.StorageName]
	h.mu.Unlock()
	if !ok {
		return fmt.Sprintf("🚫 未找到存储 `%s`\n\n已配置的 REST 存储：`%v`", action.StorageName, h.ListRESTStorages()), nil
	}

	if h.dev.DryRun {
		return h.dryRunReply(action.StorageName, "rest storage query", "GET "+action.StorageName+" API"), nil
	}

	result, err := rs.Query(ctx, action.RawMessage)
	if err != nil {
		return "", fmt.Errorf("query %s: %w", action.StorageName, err)
	}

	return h.analyzeOrEcho(ctx, action.StorageName, result.Label, result.Output), nil
}

// analyzeOrEcho returns AI analysis under normal mode, or raw output under dev mode.
func (h *Handler) analyzeOrEcho(ctx context.Context, clusterName, title, diagnostics string) string {
	output := diagnostics
	if len(output) > 200 {
		output = output[:200]
	}
	slog.Info("AI analysis with raw output", "cluster", clusterName, "title", title, "diagnostics", output, "llm enable", !h.llmDisabled.Load())
	if h.llmDisabled.Load() || h.analyzer == nil {
		return "🔕 **LLM 已关闭** · 以下为原始输出\n\n" + renderRaw(diagnostics)
	}

	ctx, cancelFunc := context.WithTimeout(ctx, time.Minute)
	defer cancelFunc()

	analysis, err := h.analyzer.Analyze(ctx, clusterName, diagnostics)
	if err != nil {
		slog.Warn("AI analysis failed, returning raw output", "cluster", clusterName, "title", title, "error", err)
		return "⚠️ **AI 分析失败** · 返回原始输出\n\n" + renderRaw(diagnostics)
	}
	return fmt.Sprintf("🤖 **AI 报告**\n\n%s", analysis)
}

// renderRaw wraps plain command output in a code fence, but passes through
// already-formatted skill output (which contains its own markdown / fences).
func renderRaw(s string) string {
	if strings.Contains(s, "```") {
		return s
	}
	return "📄 **原始输出**\n```\n" + s + "\n```"
}

func (h *Handler) dryRunReply(clusterName, action, willDo string) string {
	return fmt.Sprintf("🧪 **dry-run** · 集群 `%s`\n\n🎯 **动作** %s\n🛠 **将要执行** %s\n\n💡 `dev.dry_run = true`，未实际执行任何命令", clusterName, action, willDo)
}

// isGateway reports whether node is the gateway itself (same IP, ignoring port).
func isGateway(nodeHost, gwHost string) bool {
	return executor.HostIP(nodeHost) == executor.HostIP(gwHost)
}

func (h *Handler) replyCard(ctx context.Context, messageID string, c *card.Card) error {
	content, err := c.JSON()
	if err != nil {
		return fmt.Errorf("build card: %w", err)
	}
	resp, err := h.feishuClient.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("interactive").
				Content(content).
				Build()).
			Build())

	if err != nil {
		return fmt.Errorf("reply message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("reply failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// cardForAction wraps the handler's body string in a themed Feishu card. The
// header (emoji + title + color) is chosen from the action type; errors always
// flip to a red ⚠ header regardless of action.
func cardForAction(action intent.Action, body string, err error) *card.Card {
	if err != nil {
		return card.New("⚠️", "执行出错", card.ThemeRed).
			Body(body).
			Note("可输入 `help` 查看支持的指令")
	}

	switch action.Type {
	case intent.ActionHelp:
		return card.New("📖", "Storage Bot 使用指南", card.ThemeBlue).Body(body)
	case intent.ActionListClusters:
		return card.New("🗂", "集群与存储列表", card.ThemeBlue).Body(body)
	case intent.ActionListSkills:
		return card.New("🧰", "可用 Skills", card.ThemeBlue).Body(body)
	case intent.ActionToggleLLM:
		theme := card.ThemeGreen
		emoji := "🟢"
		if !action.ToggleLLMEnable {
			theme = card.ThemeGray
			emoji = "⚪"
		}
		return card.New(emoji, "LLM 开关", theme).Body(body)
	case intent.ActionHealth:
		return card.New("🩺", titleWithCluster("健康检查", action.ClusterName), card.ThemeTurquoise).Body(body)
	case intent.ActionLogAnalysis:
		return card.New("📜", titleWithCluster("日志分析", action.ClusterName), card.ThemePurple).Body(body)
	case intent.ActionNodeDiag:
		t := "节点诊断"
		if action.NodeName != "" {
			t = "节点诊断 · " + action.NodeName
		}
		return card.New("🖥", titleWithCluster(t, action.ClusterName), card.ThemeOrange).Body(body)
	case intent.ActionSkill:
		t := "Skill"
		if action.SkillName != "" {
			t = "Skill · " + action.SkillName
		}
		return card.New("⚙️", titleWithCluster(t, action.ClusterName), card.ThemeBlue).Body(body)
	case intent.ActionRESTStorage:
		return card.New("📦", titleWithCluster("焱融存储", action.StorageName), card.ThemeTurquoise).Body(body)
	default:
		return card.New("💬", "Storage Bot", card.ThemeBlue).Body(body)
	}
}

func titleWithCluster(label, name string) string {
	if name == "" {
		return label
	}
	return label + " · " + name
}

func (h *Handler) getKubeExecutor(clusterName string, cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if ke, ok := h.kubeCache[clusterName]; ok {
		return ke, nil
	}

	ke, err := executor.NewKubeExecutor(cfg.Kubeconfig,
		executor.WithNamespace(cfg.Namespace),
		executor.WithToolboxPodHint(cfg.ToolboxPod),
		executor.WithServerOverride(cfg.ServerOverride),
		executor.WithInsecureSkipTLSVerify(cfg.InsecureSkipTLSVerify),
	)
	if err != nil {
		return nil, err
	}
	h.kubeCache[clusterName] = ke
	return ke, nil
}

func extractText(content string) string {
	var msg struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return content
	}
	return msg.Text
}

func filterNodes(nodes []config.SSHNode, nameHint string) []config.SSHNode {
	nameHint = strings.ToLower(nameHint)
	var result []config.SSHNode
	for _, n := range nodes {
		if strings.Contains(strings.ToLower(n.Name), nameHint) {
			result = append(result, n)
		}
	}
	return result
}

// isBotMentioned checks if the bot was explicitly mentioned (not just @all).
// Returns true if mentions is empty (p2p chat) or contains a specific user mention.
// Returns false if the only mention is @all. Feishu reports @all with key
// "@_all" (and historically "_all"/"all"); accept all three to be safe.
func isBotMentioned(mentions []*larkim.MentionEvent) bool {
	if len(mentions) == 0 {
		return true // p2p chat or no mentions
	}
	for _, m := range mentions {
		if m.Key == nil {
			continue
		}
		k := *m.Key
		if k == "@_all" || k == "_all" || k == "all" {
			continue
		}
		return true // specific user mention (bot or other user)
	}
	return false // only @all
}
