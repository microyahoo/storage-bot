package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
)

type Handler struct {
	feishuClient *lark.Client
	clusterMgr   *cluster.Manager
	sshExec      *executor.SSHExecutor
	analyzer     *analyzer.Analyzer
	llm          analyzer.LLMProvider
	skills       *skill.Registry
	audit        *security.AuditLog
	kubeCache    map[string]*executor.KubeExecutor
	mu           sync.Mutex
}

func NewHandler(feishuClient *lark.Client, mgr *cluster.Manager, sshExec *executor.SSHExecutor, az *analyzer.Analyzer, llm analyzer.LLMProvider, skills *skill.Registry, audit *security.AuditLog) *Handler {
	return &Handler{
		feishuClient: feishuClient,
		clusterMgr:   mgr,
		sshExec:      sshExec,
		analyzer:     az,
		llm:          llm,
		skills:       skills,
		audit:        audit,
		kubeCache:    make(map[string]*executor.KubeExecutor),
	}
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
	action := intent.ParseWithSkills(sanitized, h.clusterMgr.List(), knownSkills)

	if intent.NeedsFallback(action) {
		slog.Info("regex parse incomplete, trying LLM fallback", "raw", sanitized)
		llmAction, err := intent.ParseWithLLM(ctx, sanitized, h.clusterMgr.List(), h.llm)
		if err != nil {
			slog.Warn("LLM intent parsing failed, using regex result", "error", err)
		} else {
			action = llmAction
		}
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

	kubeExec, err := h.getKubeExecutor(clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("connect to cluster %s: %w", clusterName, err)
	}

	diagnostics, err := kubeExec.CephHealth(ctx)
	if err != nil {
		return "", fmt.Errorf("get ceph health: %w", err)
	}

	analysis, err := h.analyzer.Analyze(ctx, clusterName, diagnostics)
	if err != nil {
		return fmt.Sprintf("**集群 %s 诊断数据:**\n```\n%s\n```\n\nAI 分析失败: %v", clusterName, diagnostics, err), nil
	}

	return fmt.Sprintf("**集群 %s 健康检查报告**\n\n%s", clusterName, analysis), nil
}

func (h *Handler) handleLogAnalysis(ctx context.Context, action intent.Action) (string, error) {
	if action.ClusterName == "" {
		return "请指定集群名称，例如: analyze logs cluster-01\n\n" + h.listClusters(), nil
	}

	clusterName, clusterCfg, err := h.clusterMgr.FindByPrefix(action.ClusterName)
	if err != nil {
		return "", err
	}

	if len(clusterCfg.SSHNodes) == 0 {
		return fmt.Sprintf("集群 %s 没有配置 SSH 节点", clusterName), nil
	}

	var allLogs []string
	targetNodes := clusterCfg.SSHNodes
	if action.NodeName != "" {
		targetNodes = filterNodes(clusterCfg.SSHNodes, action.NodeName)
		if len(targetNodes) == 0 {
			return fmt.Sprintf("集群 %s 中未找到节点 %s", clusterName, action.NodeName), nil
		}
	}

	for _, node := range targetNodes {
		logs, err := h.sshExec.CollectLogs(ctx, node)
		if err != nil {
			allLogs = append(allLogs, fmt.Sprintf("=== Node %s ERROR ===\n%v", node.Name, err))
		} else {
			allLogs = append(allLogs, fmt.Sprintf("=== Node %s Logs ===\n%s", node.Name, logs))
		}
	}

	diagnostics := strings.Join(allLogs, "\n\n")
	analysis, err := h.analyzer.Analyze(ctx, clusterName, diagnostics)
	if err != nil {
		return fmt.Sprintf("AI 分析失败: %v\n\n原始日志已收集 (%d bytes)", err, len(diagnostics)), nil
	}

	return fmt.Sprintf("**集群 %s 日志分析报告**\n\n%s", clusterName, analysis), nil
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

	nodes := filterNodes(clusterCfg.SSHNodes, action.NodeName)
	if len(nodes) == 0 {
		return fmt.Sprintf("集群 %s 中未找到节点 %s", clusterName, action.NodeName), nil
	}

	node := nodes[0]
	diagnostics, err := h.sshExec.NodeDiagnostics(ctx, node)
	if err != nil {
		return "", fmt.Errorf("node diagnostics for %s: %w", node.Name, err)
	}

	analysis, err := h.analyzer.Analyze(ctx, clusterName, diagnostics)
	if err != nil {
		return fmt.Sprintf("AI 分析失败: %v\n\n原始数据:\n```\n%s\n```", err, diagnostics), nil
	}

	return fmt.Sprintf("**集群 %s 节点 %s 诊断报告**\n\n%s", clusterName, node.Name, analysis), nil
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

	kubeExec, err := h.getKubeExecutor(clusterName, clusterCfg)
	if err != nil {
		return "", fmt.Errorf("connect to cluster %s: %w", clusterName, err)
	}

	nodes := make([]skill.SSHTarget, 0, len(clusterCfg.SSHNodes))
	for _, n := range clusterCfg.SSHNodes {
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
		KubeExec:    kubeExec,
		SSHExec:     h.sshExec,
		Nodes:       nodes,
	}

	output, err := s.Execute(sc)
	if err != nil {
		return "", fmt.Errorf("skill %s execution failed: %w", action.SkillName, err)
	}

	analysis, err := h.analyzer.Analyze(ctx, clusterName, output)
	if err != nil {
		return fmt.Sprintf("**集群 %s — %s**\n\n```\n%s\n```\n\nAI 分析失败: %v", clusterName, s.Name(), output, err), nil
	}

	return fmt.Sprintf("**集群 %s — %s 报告**\n\n%s", clusterName, s.Description(), analysis), nil
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

	ke, err := executor.NewKubeExecutor(cfg.Kubeconfig, cfg.Namespace, cfg.ToolboxPod)
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
