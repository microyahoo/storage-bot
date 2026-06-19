package skill

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/config"
)

const rgwBucketsDataSuffix = ".rgw.buckets.data"

// kernelKeywordRe constrains the keyword we splice into the remote shell. The
// intent parser already filters to this alphabet; this is a defensive recheck.
var kernelKeywordRe = regexp.MustCompile(`^[A-Za-z0-9_.:\-]+$`)

type OSDStatus struct{}

func (s *OSDStatus) Name() string { return "osd_status" }
func (s *OSDStatus) Description() string {
	return "查看 OSD 状态、down/out 的 OSD 及修复建议"
}
func (s *OSDStatus) Execute(sc *Context) (string, error) {
	commands := []string{"osd status", "osd tree", "osd df"}
	return runCephCommands(sc, commands)
}

type PGStatus struct{}

func (s *PGStatus) Name() string        { return "pg_status" }
func (s *PGStatus) Description() string { return "查看 PG 状态、不一致/降级的 PG" }
func (s *PGStatus) Execute(sc *Context) (string, error) {
	commands := []string{"pg stat", "pg dump_stuck unclean", "pg dump_stuck inactive", "pg dump_stuck stale"}
	return runCephCommands(sc, commands)
}

type PoolStatus struct{}

func (s *PoolStatus) Name() string        { return "pool_status" }
func (s *PoolStatus) Description() string { return "查看所有存储池状态和配置" }
func (s *PoolStatus) Execute(sc *Context) (string, error) {
	commands := []string{"osd pool ls detail", "df detail"}
	return runCephCommands(sc, commands)
}

type CapacityCheck struct{}

func (s *CapacityCheck) Name() string { return "capacity" }
func (s *CapacityCheck) Description() string {
	return "检查集群容量使用和各 OSD 磁盘使用率"
}
func (s *CapacityCheck) Execute(sc *Context) (string, error) {
	commands := []string{"df", "osd df tree"}
	return runCephCommands(sc, commands)
}

type SlowOps struct{}

func (s *SlowOps) Name() string        { return "slow_ops" }
func (s *SlowOps) Description() string { return "检查慢请求 (slow ops) 和阻塞的操作" }
func (s *SlowOps) Execute(sc *Context) (string, error) {
	commands := []string{"daemon osd.* dump_ops_in_flight", "health detail"}
	output, err := runCephCommands(sc, []string{"health detail"})
	if err != nil {
		return "", err
	}
	_ = commands
	return output, nil
}

type CrashReport struct{}

func (s *CrashReport) Name() string        { return "crash" }
func (s *CrashReport) Description() string { return "查看 Ceph 崩溃报告" }
func (s *CrashReport) Execute(sc *Context) (string, error) {
	commands := []string{"crash ls", "crash ls-new"}
	return runCephCommands(sc, commands)
}

// CrashInfo shows `ceph crash ls` and, if there is at least one entry, the full
// `ceph crash info <id>` for the most recent crash. Saves a round-trip for
// users who only care about the latest stack trace.
type CrashInfo struct{}

func (s *CrashInfo) Name() string        { return "crash_info" }
func (s *CrashInfo) Description() string { return "列出 crash 并展示最近一条的完整信息" }
func (s *CrashInfo) Execute(sc *Context) (string, error) {
	if sc.KubeExec == nil {
		return "", fmt.Errorf("no kubernetes connection available")
	}

	lsOut, err := sc.KubeExec.RunCephCommand(sc.Ctx, "crash", "ls")
	if err != nil {
		return fmt.Sprintf("📡 `ceph crash ls` ❌\n```\n%v\n```", err), nil
	}

	latest := latestCrashID(lsOut)
	header := fmt.Sprintf("📡 `ceph crash ls`\n```\n%s\n```", strings.TrimRight(lsOut, "\n"))
	if latest == "" {
		return header + "\n\n✅ 没有发现 crash", nil
	}

	infoOut, err := sc.KubeExec.RunCephCommand(sc.Ctx, "crash", "info", latest)
	if err != nil {
		return fmt.Sprintf("%s\n\n📡 `ceph crash info %s` ❌\n```\n%v\n```", header, latest, err), nil
	}
	return fmt.Sprintf("%s\n\n📡 `ceph crash info %s`\n```\n%s\n```",
		header, latest, strings.TrimRight(infoOut, "\n")), nil
}

// latestCrashID extracts the last crash ID from `ceph crash ls` output. Output
// shape (sorted oldest-first):
//
//	ID                                                                ENTITY     NEW
//	2024-01-02T12:34:56.789012Z_abc-def...                            osd.5      *
//
// We scan non-empty lines and return the first whitespace-delimited token of
// the last data row — skipping the header row (which starts with "ID").
func latestCrashID(out string) string {
	var lastID string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "ID" {
			continue
		}
		lastID = fields[0]
	}
	return lastID
}

type MonStatus struct{}

func (s *MonStatus) Name() string        { return "mon_status" }
func (s *MonStatus) Description() string { return "检查 Monitor 仲裁状态和 leader 选举" }
func (s *MonStatus) Execute(sc *Context) (string, error) {
	commands := []string{"mon stat", "quorum_status"}
	return runCephCommands(sc, commands)
}

type IOStat struct{}

func (s *IOStat) Name() string        { return "io_stat" }
func (s *IOStat) Description() string { return "查看节点磁盘 IO 统计" }
func (s *IOStat) Execute(sc *Context) (string, error) {
	nodes, err := resolveNodes(sc.Nodes, sc.NodeName)
	if err != nil {
		return err.Error(), nil
	}

	var results []string
	for _, node := range nodes {
		sshNode := config.SSHNode{
			Name:    node.Name,
			Host:    node.Host,
			User:    node.User,
			KeyFile: node.KeyFile,
		}
		output, err := sc.RunOnNode(sshNode, "iostat -x 1 3 2>/dev/null || cat /proc/diskstats")
		if err != nil {
			results = append(results, fmt.Sprintf("📌 **%s** ❌\n```\n%v\n```", node.Name, err))
		} else {
			results = append(results, fmt.Sprintf("📌 **%s**\n```\n%s\n```", node.Name, output))
		}
	}
	return strings.Join(results, "\n\n"), nil
}

type KernelLogs struct{}

func (s *KernelLogs) Name() string { return "kernel_logs" }
func (s *KernelLogs) Description() string {
	return "查看节点 kernel 日志（过滤掉 systemd/kubelet 等无关行）"
}
func (s *KernelLogs) Execute(sc *Context) (string, error) {
	nodes, err := resolveNodes(sc.Nodes, sc.NodeName)
	if err != nil {
		return err.Error(), nil
	}

	count := "200"
	if v := strings.TrimSpace(sc.Args["count"]); v != "" {
		count = v
	} else if v := strings.TrimSpace(sc.Args["n"]); v != "" {
		count = v
	}
	// guard count: digits only, otherwise it would get spliced into the remote
	// shell command unchecked.
	if _, err := strconv.Atoi(count); err != nil {
		return fmt.Sprintf("🚫 count 必须是正整数：%q", count), nil
	}

	keyword := strings.TrimSpace(sc.Args["keyword"])
	// keep keyword to alnum + a few harmless punctuation chars so it can be
	// spliced into the shell pipeline without quoting tricks. The intent parser
	// already restricts the alphabet; this is a belt-and-braces check.
	if keyword != "" && !kernelKeywordRe.MatchString(keyword) {
		return fmt.Sprintf("🚫 keyword 含非法字符：%q（只允许字母/数字/._:-）", keyword), nil
	}

	// The SSH validator inspects parts[0] of the command and rejects unknown
	// leading tokens (so a leading `{` or `bash` would be blocked). Keep the
	// command linear, starting with `journalctl`. The `||` fallback uses `grep`,
	// which is also on the safe list, so the validator's metachar check is
	// satisfied by parts[0] = "journalctl".
	//
	// Pipeline shape:
	//   journalctl -k -n COUNT --no-pager 2>/dev/null | grep -i KEYWORD | tail -n COUNT
	//     || grep ' kernel: ' /var/log/{messages,syslog} 2>/dev/null
	//        | grep -i KEYWORD | tail -n COUNT
	grepKw := ""
	if keyword != "" {
		grepKw = fmt.Sprintf(" | grep -i -- %s", keyword)
	}
	cmd := fmt.Sprintf(
		"journalctl -k -n %[1]s --no-pager 2>/dev/null%[2]s | tail -n %[1]s "+
			"|| grep -E ' kernel: ' /var/log/messages /var/log/syslog 2>/dev/null%[2]s | tail -n %[1]s",
		count, grepKw,
	)

	var results []string
	for _, node := range nodes {
		sshNode := config.SSHNode{
			Name:    node.Name,
			Host:    node.Host,
			User:    node.User,
			KeyFile: node.KeyFile,
		}
		header := fmt.Sprintf("📌 **%s** · 最近 %s 条 kernel 日志", node.Name, count)
		if keyword != "" {
			header += fmt.Sprintf(" · 关键字 `%s`", keyword)
		}
		output, err := sc.RunOnNode(sshNode, cmd)
		if err != nil {
			results = append(results, fmt.Sprintf("%s ❌\n```\n%v\n```", header, err))
			continue
		}
		if strings.TrimSpace(output) == "" {
			output = "(无匹配)"
		}
		results = append(results, fmt.Sprintf("%s\n```\n%s\n```", header, output))
	}
	return strings.Join(results, "\n\n"), nil
}

type NICInfo struct{}

func (s *NICInfo) Name() string        { return "nic_info" }
func (s *NICInfo) Description() string { return "查看节点网卡信息（ip link show）" }
func (s *NICInfo) Execute(sc *Context) (string, error) {
	nodes, err := resolveNodes(sc.Nodes, sc.NodeName)
	if err != nil {
		return err.Error(), nil
	}

	var results []string
	for _, node := range nodes {
		sshNode := config.SSHNode{
			Name:    node.Name,
			Host:    node.Host,
			User:    node.User,
			KeyFile: node.KeyFile,
		}
		output, err := sc.RunOnNode(sshNode, "ip -br link show | grep -E 'lo|cali|tun|ipvs' -v")
		if err != nil {
			results = append(results, fmt.Sprintf("📌 **%s** ❌\n```\n%v```", node.Name, err))
			continue
		}
		results = append(results, fmt.Sprintf("📌 **%s**\n```\n%s```", node.Name, output))
	}
	return strings.Join(results, "\n\n"), nil
}

// BondStatus enumerates /proc/net/bonding/bond* on each node and reports the
// Link Failure Count per slave interface. A non-zero count usually means the
// link has flapped — the headline signal users want from `cat /proc/net/bonding/bondN`.
type BondStatus struct{}

func (s *BondStatus) Name() string { return "bond_status" }
func (s *BondStatus) Description() string {
	return "查询节点所有 bond 网口的 Link Failure Count"
}
func (s *BondStatus) Execute(sc *Context) (string, error) {
	nodes, err := resolveNodes(sc.Nodes, sc.NodeName)
	if err != nil {
		return err.Error(), nil
	}

	// Single grep across all bond files. parts[0] = "grep" → safe-list pass.
	// `-H` prints "filename:line" so we can attribute counts back to each bond
	// when the node has more than one. `2>/dev/null` swallows the "no such
	// file" stderr if bonding isn't configured. The trailing `|| echo ...`
	// keeps the output non-empty so users see *why* there's nothing to show.
	cmd := "grep -H -E '^(Slave Interface|Link Failure Count|Bonding Mode|MII Status)' " +
		"/proc/net/bonding/bond* 2>/dev/null || echo '(no bonds configured)'"

	var results []string
	for _, node := range nodes {
		sshNode := config.SSHNode{
			Name:    node.Name,
			Host:    node.Host,
			User:    node.User,
			KeyFile: node.KeyFile,
		}
		output, err := sc.RunOnNode(sshNode, cmd)
		if err != nil {
			results = append(results, fmt.Sprintf("📌 **%s** ❌\n```\n%v\n```", node.Name, err))
			continue
		}
		results = append(results, fmt.Sprintf("📌 **%s**\n%s", node.Name, formatBondReport(output)))
	}
	return strings.Join(results, "\n\n"), nil
}

// formatBondReport reshapes grep output of the form
//
//	/proc/net/bonding/bond0:Bonding Mode: IEEE 802.3ad ...
//	/proc/net/bonding/bond0:Slave Interface: eth0
//	/proc/net/bonding/bond0:MII Status: up
//	/proc/net/bonding/bond0:Link Failure Count: 0
//
// into a per-bond grouped, slave-aligned table. Slaves with a non-zero
// failure count get a ⚠ marker so they jump out.
func formatBondReport(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "(no bonds configured)" {
		return "```\n(no bonds configured)\n```"
	}

	type slave struct {
		name      string
		miiStatus string
		failures  string
	}
	type bond struct {
		name    string
		mode    string
		slaves  []*slave
		current *slave
	}

	bondsByName := map[string]*bond{}
	var order []string

	for _, line := range strings.Split(raw, "\n") {
		// Each line looks like "/proc/net/bonding/bondN:<key>: <value>"
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		path := line[:colon]
		body := strings.TrimSpace(line[colon+1:])
		name := strings.TrimPrefix(path, "/proc/net/bonding/")

		b, ok := bondsByName[name]
		if !ok {
			b = &bond{name: name}
			bondsByName[name] = b
			order = append(order, name)
		}

		key, val, found := strings.Cut(body, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch key {
		case "Bonding Mode":
			b.mode = val
		case "Slave Interface":
			sl := &slave{name: val}
			b.slaves = append(b.slaves, sl)
			b.current = sl
		case "MII Status":
			if b.current != nil {
				b.current.miiStatus = val
			}
		case "Link Failure Count":
			if b.current != nil {
				b.current.failures = val
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("```\n")
	for _, name := range order {
		b := bondsByName[name]
		sb.WriteString(fmt.Sprintf("● %s", b.name))
		if b.mode != "" {
			sb.WriteString(fmt.Sprintf("  (mode: %s)", b.mode))
		}
		sb.WriteString("\n")
		if len(b.slaves) == 0 {
			sb.WriteString("    (no slaves)\n")
			continue
		}
		for _, sl := range b.slaves {
			marker := "  "
			if sl.failures != "" && sl.failures != "0" {
				marker = "⚠ "
			}
			sb.WriteString(fmt.Sprintf("    %s%-10s  MII=%-5s  Link Failure Count=%s\n",
				marker, sl.name, sl.miiStatus, sl.failures))
		}
	}
	sb.WriteString("```")
	return sb.String()
}

type ListNodes struct{}

func (s *ListNodes) Name() string { return "list_nodes" }
func (s *ListNodes) Description() string {
	return "获取集群所有节点信息（名称、IP、角色）"
}
func (s *ListNodes) Execute(sc *Context) (string, error) {
	if sc.KubeExec == nil {
		return "", fmt.Errorf("no kubernetes connection available")
	}

	nodes, err := sc.KubeExec.DiscoverNodes(sc.Ctx)
	if err != nil {
		return "", fmt.Errorf("discover nodes: %w", err)
	}

	if len(nodes) == 0 {
		return "🚫 未发现节点", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🖧 集群 **`%s`** · 共 **%d** 个节点\n", sc.ClusterName, len(nodes)))
	for _, n := range nodes {
		sb.WriteString(fmt.Sprintf("　🟢 `%s` &nbsp;→ &nbsp;`%s`\n", n.Name, n.InternalIP))
	}
	return sb.String(), nil
}

func runCephCommands(sc *Context, commands []string) (string, error) {
	if sc.KubeExec == nil {
		return "", fmt.Errorf("no kubernetes connection available")
	}

	var results []string
	for _, cmd := range commands {
		args := strings.Fields(cmd)
		output, err := sc.KubeExec.RunCephCommand(sc.Ctx, args...)
		if err != nil {
			results = append(results, fmt.Sprintf("📡 `ceph %s` ❌\n```\n%v\n```", cmd, err))
		} else {
			results = append(results, fmt.Sprintf("📡 `ceph %s`\n```\n%s\n```", cmd, output))
		}
	}
	return strings.Join(results, "\n\n"), nil
}

// --- New skills ---

type GetFSID struct{}

func (s *GetFSID) Name() string        { return "get_fsid" }
func (s *GetFSID) Description() string { return "查询 Ceph 集群的 FSID" }
func (s *GetFSID) Execute(sc *Context) (string, error) {
	return runCephCommands(sc, []string{"fsid"})
}

type GetMonIPs struct{}

func (s *GetMonIPs) Name() string        { return "get_mon_ips" }
func (s *GetMonIPs) Description() string { return "查询 Ceph 集群的 Monitor IP 列表" }
func (s *GetMonIPs) Execute(sc *Context) (string, error) {
	return runCephCommands(sc, []string{"mon dump"})
}

type SetNoBackfillRebalanceRecover struct{}

func (s *SetNoBackfillRebalanceRecover) Name() string { return "set_no_backfill" }
func (s *SetNoBackfillRebalanceRecover) Description() string {
	return "设置 nobackfill + norebalance + norecover flags（暂停数据迁移和恢复）"
}
func (s *SetNoBackfillRebalanceRecover) Execute(sc *Context) (string, error) {
	return runCephCommands(sc, []string{
		"osd set nobackfill",
		"osd set norebalance",
		"osd set norecover",
		"health",
	})
}

type UnsetNoBackfillRebalanceRecover struct{}

func (s *UnsetNoBackfillRebalanceRecover) Name() string { return "unset_no_backfill" }
func (s *UnsetNoBackfillRebalanceRecover) Description() string {
	return "取消 nobackfill + norebalance + norecover flags（恢复数据迁移和恢复）"
}
func (s *UnsetNoBackfillRebalanceRecover) Execute(sc *Context) (string, error) {
	return runCephCommands(sc, []string{
		"osd unset nobackfill",
		"osd unset norebalance",
		"osd unset norecover",
		"health",
	})
}

type SetNoout struct{}

func (s *SetNoout) Name() string        { return "set_noout" }
func (s *SetNoout) Description() string { return "设置 noout flag（防止 OSD 被标记为 out）" }
func (s *SetNoout) Execute(sc *Context) (string, error) {
	return runCephCommands(sc, []string{"osd set noout", "health"})
}

type UnsetNoout struct{}

func (s *UnsetNoout) Name() string { return "unset_noout" }
func (s *UnsetNoout) Description() string {
	return "取消 noout flag（恢复自动 OSD out 检测）"
}
func (s *UnsetNoout) Execute(sc *Context) (string, error) {
	return runCephCommands(sc, []string{"osd unset noout", "health"})
}

// OptimizeRGWBucketsPG optimizes PG placement for the rgw.buckets.data pool
// using osdmaptool --upmap. It runs entirely inside the toolbox pod.
// Optional arg "max" controls --upmap-max (default 100).
type OptimizeRGWBucketsPG struct{}

func (s *OptimizeRGWBucketsPG) Name() string { return "optimize_rgw_pg" }
func (s *OptimizeRGWBucketsPG) Description() string {
	return "优化以 " + rgwBucketsDataSuffix + " 结尾的存储池的 PG 分布（osdmaptool upmap）"
}
func (s *OptimizeRGWBucketsPG) Execute(sc *Context) (string, error) {
	if sc.KubeExec == nil {
		return "", fmt.Errorf("no kubernetes connection available")
	}

	max := "100"
	if v, ok := sc.Args["max"]; ok && v != "" {
		max = v
	}

	// Find the actual pool name ending with rgwBucketsDataSuffix.
	poolList, err := sc.KubeExec.RunCephCommand(sc.Ctx, "osd", "pool", "ls")
	if err != nil {
		return "", fmt.Errorf("list pools: %w", err)
	}
	pool := ""
	for _, line := range strings.Split(poolList, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, rgwBucketsDataSuffix) {
			pool = line
			break
		}
	}
	if pool == "" {
		return fmt.Sprintf("🚫 未找到以 `%s` 结尾的存储池", rgwBucketsDataSuffix), nil
	}

	script := fmt.Sprintf(`set -e
ceph osd getmap -o /tmp/om
osdmaptool /tmp/om --upmap /tmp/out.txt --upmap-deviation 1 --upmap-max %s --upmap-pool %q
echo "=== upmap commands ==="
cat /tmp/out.txt
echo "=== applying ==="
bash /tmp/out.txt
echo "done"`, max, pool)

	ctx, cancelFunc := context.WithTimeout(sc.Ctx, 150*time.Second)
	defer cancelFunc()

	output, err := sc.KubeExec.RunShellScript(ctx, script)
	if err != nil {
		return "", fmt.Errorf("optimize rgw pg: %w", err)
	}
	return fmt.Sprintf("🪄 **优化 PG 分布** · pool `%s`\n```\n%s\n```", pool, output), nil
}

// filterNodes returns nodes whose name contains nameHint (case-insensitive).
func filterNodes(nodes []SSHTarget, nameHint string) []SSHTarget {
	hint := strings.ToLower(nameHint)
	var result []SSHTarget
	for _, n := range nodes {
		if strings.Contains(strings.ToLower(n.Name), hint) {
			result = append(result, n)
		}
	}
	return result
}

// resolveNodes returns the nodes to operate on. If nodeName is empty all nodes are returned.
// If nodeName is set but matches nothing, an error describing the available nodes is returned.
func resolveNodes(nodes []SSHTarget, nodeName string) ([]SSHTarget, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("集群中没有可用的 SSH 节点")
	}
	if nodeName == "" {
		return nodes, nil
	}
	matched := filterNodes(nodes, nodeName)
	if len(matched) > 0 {
		return matched, nil
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	return nil, fmt.Errorf("集群中未找到节点 %q，可用节点: %s", nodeName, strings.Join(names, ", "))
}

// RestartMon restarts a specific mon by deleting its pod (rook recreates it).
type RestartMon struct{}

func (s *RestartMon) Name() string { return "restart_mon" }
func (s *RestartMon) Description() string {
	return "重启指定 mon（删除其 pod，rook 自动重建）"
}
func (s *RestartMon) Execute(sc *Context) (string, error) {
	return restartCephDaemon(sc, "mon")
}

// RestartMgr restarts a specific mgr by deleting its pod (rook recreates it).
type RestartMgr struct{}

func (s *RestartMgr) Name() string { return "restart_mgr" }
func (s *RestartMgr) Description() string {
	return "重启指定 mgr（删除其 pod，rook 自动重建）"
}
func (s *RestartMgr) Execute(sc *Context) (string, error) {
	return restartCephDaemon(sc, "mgr")
}

// restartCephDaemon implements the shared restart-by-pod-delete flow for mon/mgr.
//
//	Args["id"]  — daemon id (a/b/c). Required to act; if empty, list candidates.
//	Args["yes"] — "true" to actually delete; otherwise preview only (safety gate).
func restartCephDaemon(sc *Context, daemon string) (string, error) {
	if sc.KubeExec == nil {
		return "", fmt.Errorf("no kubernetes connection available")
	}
	pods, err := sc.KubeExec.ListCephPods(sc.Ctx, daemon)
	if err != nil {
		return "", err
	}
	if len(pods) == 0 {
		return fmt.Sprintf("未找到任何 %s pod", daemon), nil
	}

	id := strings.TrimSpace(sc.Args["id"])
	if id == "" {
		// No id: list candidates so the operator can pick one.
		var b strings.Builder
		fmt.Fprintf(&b, "请指定要重启的 %s id（例如 `重启 %s a %s`）。当前 %s 列表：\n", daemon, daemon, sc.ClusterName, daemon)
		for _, p := range pods {
			fmt.Fprintf(&b, "- `%s`（id=%s, node=%s, %s）\n", p.Name, p.DaemonID, p.Node, p.Status)
		}
		return b.String(), nil
	}

	// Find the pod matching the requested id (index into the slice; avoids
	// importing executor just for the element type).
	idx := -1
	var availIDs []string
	for i, p := range pods {
		availIDs = append(availIDs, p.DaemonID)
		if p.DaemonID == id {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Sprintf("未找到 %s.%s。可用 id：%s", daemon, id, strings.Join(availIDs, ", ")), nil
	}
	target := pods[idx]

	if sc.Args["yes"] != "true" {
		return fmt.Sprintf("⚠️ 将重启 **%s.%s**（删除 pod `%s` @ %s，rook 会自动重建）。\n"+
			"这是写操作，确认请重发并加 `--yes`：`重启 %s %s %s --yes`",
			daemon, id, target.Name, target.Node, daemon, id, sc.ClusterName), nil
	}

	if err := sc.KubeExec.DeletePod(sc.Ctx, target.Name); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ 已删除 pod `%s`（%s.%s @ %s），rook 正在重建中。\n用 `mon status` / `ceph -s` 确认恢复。",
		target.Name, daemon, id, target.Node), nil
}
