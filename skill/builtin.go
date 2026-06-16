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
