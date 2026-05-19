package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/microyahoo/storage-bot/analyzer"
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
}

func NewHandler(feishuClient *lark.Client, mgr *cluster.Manager, sshExec *executor.SSHExecutor, az *analyzer.Analyzer, llm analyzer.LLMProvider, skills *skill.Registry, audit *security.AuditLog, dev config.DevConfig) *Handler {
	return &Handler{
		feishuClient: feishuClient,
		clusterMgr:   mgr,
		sshExec:      sshExec,
		analyzer:     az,
		llm:          llm,
		skills:       skills,
		restStorages: make(map[string]*storage.RESTSkill),
		audit:        audit,
		dev:          dev,
		kubeCache:    make(map[string]*executor.KubeExecutor),
	}
}

func (h *Handler) AddRESTStorage(name string, s *storage.RESTSkill) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restStorages[name] = s
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

	if intent.NeedsFallback(action) && !h.dev.DisableLLM && h.llm != nil {
		slog.Info("regex parse incomplete, trying LLM fallback", "raw", sanitized)
		llmAction, err := intent.ParseWithLLM(ctx, sanitized, h.clusterMgr.List(), h.llm)
		if err != nil {
			slog.Warn("LLM intent parsing failed, using regex result", "error", err)
		} else {
			action = llmAction
		}
	} else if intent.NeedsFallback(action) && h.dev.DisableLLM {
		slog.Info("[dev] LLM fallback disabled, using regex result as-is", "raw", sanitized)
	}

	slog.Info("parsed intent", "type", action.Type, "cluster", action.ClusterName, "node", action.NodeName)

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
		reply = fmt.Sprintf("执行出错: %v", err)
	}

	if h.audit != nil {
		h.audit.Record(userID, action.ClusterName, action.Type.String(), text, status)
	}

	return h.replyMessage(ctx, *msg.MessageId, reply)
}

func (h *Handler) handleHealth(ctx context.Context, action intent.Action) (string, error) {
	if action.ClusterName == "" {
		return "请指定集群名称，例如: check cluster-01 health\n\n" + h.listClusters(), nil
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
		return "请指定集群名称，例如: analyze logs cluster-01\n\n" + h.listClusters(), nil
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
		return fmt.Sprintf("集群 %s 没有配置 SSH 节点，请配置 gateway_node 或 ssh_nodes", clusterName), nil
	}

	var allLogs []string
	targetNodes := allSSHNodes
	if action.NodeName != "" {
		targetNodes = filterNodes(allSSHNodes, action.NodeName)
		if len(targetNodes) == 0 {
			return fmt.Sprintf("集群 %s 中未找到节点 %s", clusterName, action.NodeName), nil
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
		return "请指定集群名称，例如: check node-1 cluster-01\n\n" + h.listClusters(), nil
	}

	clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
	if err != nil {
		return "", err
	}

	if action.NodeName == "" {
		return "请指定节点名称，例如: check node-1 cluster-01", nil
	}

	allSSHNodes, err := h.clusterMgr.ResolveSSHNodes(ctx, clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("resolve nodes for %s: %w", clusterName, err)
	}

	nodes := filterNodes(allSSHNodes, action.NodeName)
	if len(nodes) == 0 {
		return fmt.Sprintf("集群 %s 中未找到节点 %s", clusterName, action.NodeName), nil
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
	return `**Storage Bot 使用指南**

支持的命令（支持中英文自然语言输入）：

**集群列表**: list clusters / 列表集群 / 有哪些集群
**健康检查**: check cluster-01 / 看看01集群怎么了 / cluster-01有问题吗
**日志分析**: analyze logs cluster-01 / 分析01的日志
**节点诊断**: check node-1 cluster-01 / 看看01集群的node-1
**Skill**: osd cluster-01 / 看看01的pg状态 / cluster-02容量
**Skill列表**: list skills / 有哪些技能

示例：
  @bot 帮我看看cluster-01的状态
  @bot 01集群有什么问题
  @bot 分析一下cluster-02的日志
  @bot cluster-05 node-3 磁盘状态
  @bot cluster-01 osd状态
  @bot 看看cluster-02的容量`
}

func (h *Handler) listClusters() string {
	clusters := h.clusterMgr.List()
	if len(clusters) == 0 {
		return "当前没有配置任何集群"
	}
	var sb strings.Builder
	sb.WriteString("**已配置的集群列表:**\n")
	for _, name := range clusters {
		sb.WriteString(fmt.Sprintf("  %s\n", name))
	}
	return sb.String()
}

func (h *Handler) listSkills() string {
	skills := h.skills.List()
	if len(skills) == 0 {
		return "当前没有注册任何 Skill"
	}
	var sb strings.Builder
	sb.WriteString("**可用的 Skills:**\n")
	for _, s := range skills {
		sb.WriteString(fmt.Sprintf("  **%s** — %s\n", s.Name(), s.Description()))
	}
	return sb.String()
}

func (h *Handler) handleSkill(ctx context.Context, action intent.Action) (string, error) {
	s, ok := h.skills.Get(action.SkillName)
	if !ok {
		return fmt.Sprintf("未找到 Skill: %s\n\n%s", action.SkillName, h.listSkills()), nil
	}

	if action.ClusterName == "" {
		return fmt.Sprintf("请指定集群名称来执行 Skill %s\n\n%s", action.SkillName, h.listClusters()), nil
	}

	clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
	if err != nil {
		return "", err
	}

	if h.dev.DryRun {
		return h.dryRunReply(clusterName, "skill: "+s.Name(), s.Description()), nil
	}

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
		NodeName:    action.NodeName,
		Gateway:     clusterCfg.GatewayNode,
		KubeExec:    kubeExec,
		SSHExec:     h.sshExec,
		Nodes:       nodes,
	}

	output, err := s.Execute(sc)
	if err != nil {
		return "", fmt.Errorf("skill %s execution failed: %w", action.SkillName, err)
	}

	// list_nodes returns plain text, no LLM analysis needed
	if action.SkillName == "list_nodes" {
		return output, nil
	}

	return h.analyzeOrEcho(ctx, clusterName, s.Description(), output), nil
}

func (h *Handler) handleRESTStorage(ctx context.Context, action intent.Action) (string, error) {
	h.mu.Lock()
	rs, ok := h.restStorages[action.StorageName]
	h.mu.Unlock()
	if !ok {
		return fmt.Sprintf("未找到存储 %s，已配置的 REST 存储: %v", action.StorageName, h.ListRESTStorages()), nil
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
	if h.dev.DisableLLM || h.analyzer == nil {
		return fmt.Sprintf("**集群 %s — %s [dev mode, LLM disabled]**\n\n```\n%s\n```", clusterName, title, diagnostics)
	}

	analysis, err := h.analyzer.Analyze(ctx, clusterName, diagnostics)
	if err != nil {
		return fmt.Sprintf("**集群 %s — %s**\n\n```\n%s\n```\n\nAI 分析失败: %v", clusterName, title, diagnostics, err)
	}
	return fmt.Sprintf("**集群 %s — %s 报告**\n\n%s", clusterName, title, analysis)
}

func (h *Handler) dryRunReply(clusterName, action, willDo string) string {
	return fmt.Sprintf("**[dry-run] 集群 %s**\n\n动作: %s\n将要执行: %s\n\n(dev.dry_run = true, 未实际执行任何命令)", clusterName, action, willDo)
}

// hostIP strips the port from a "host:port" string, returning just the IP/hostname.
// If there is no port, the input is returned as-is.
func hostIP(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// isGateway reports whether node is the gateway itself (same IP, ignoring port).
func isGateway(nodeHost, gwHost string) bool {
	return hostIP(nodeHost) == hostIP(gwHost)
}

func (h *Handler) replyMessage(ctx context.Context, messageID, content string) error {
	body := map[string]string{"text": content}
	contentJSON, _ := json.Marshal(body)

	resp, err := h.feishuClient.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("text").
				Content(string(contentJSON)).
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

func (h *Handler) getKubeExecutor(clusterName string, cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if ke, ok := h.kubeCache[clusterName]; ok {
		return ke, nil
	}

	ke, err := executor.NewKubeExecutorWithOptions(executor.KubeExecutorOptions{
		KubeconfigPath:        cfg.Kubeconfig,
		Namespace:             cfg.Namespace,
		ToolboxPodHint:        cfg.ToolboxPod,
		ServerOverride:        cfg.ServerOverride,
		InsecureSkipTLSVerify: cfg.InsecureSkipTLSVerify,
	})
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
