package skill

import (
	"fmt"
	"strings"

	"github.com/microyahoo/storage-bot/config"
)

type OSDStatus struct{}

func (s *OSDStatus) Name() string        { return "osd_status" }
func (s *OSDStatus) Description() string  { return "查看 OSD 状态、down/out 的 OSD 及修复建议" }
func (s *OSDStatus) Execute(sc *Context) (string, error) {
	commands := []string{"osd status", "osd tree", "osd df"}
	return runCephCommands(sc, commands)
}

type PGStatus struct{}

func (s *PGStatus) Name() string        { return "pg_status" }
func (s *PGStatus) Description() string  { return "查看 PG 状态、不一致/降级的 PG" }
func (s *PGStatus) Execute(sc *Context) (string, error) {
	commands := []string{"pg stat", "pg dump_stuck unclean", "pg dump_stuck inactive", "pg dump_stuck stale"}
	return runCephCommands(sc, commands)
}

type PoolStatus struct{}

func (s *PoolStatus) Name() string        { return "pool_status" }
func (s *PoolStatus) Description() string  { return "查看所有存储池状态和配置" }
func (s *PoolStatus) Execute(sc *Context) (string, error) {
	commands := []string{"osd pool ls detail", "df detail"}
	return runCephCommands(sc, commands)
}

type CapacityCheck struct{}

func (s *CapacityCheck) Name() string        { return "capacity" }
func (s *CapacityCheck) Description() string  { return "检查集群容量使用和各 OSD 磁盘使用率" }
func (s *CapacityCheck) Execute(sc *Context) (string, error) {
	commands := []string{"df", "osd df tree"}
	return runCephCommands(sc, commands)
}

type SlowOps struct{}

func (s *SlowOps) Name() string        { return "slow_ops" }
func (s *SlowOps) Description() string  { return "检查慢请求 (slow ops) 和阻塞的操作" }
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
func (s *CrashReport) Description() string  { return "查看 Ceph 崩溃报告" }
func (s *CrashReport) Execute(sc *Context) (string, error) {
	commands := []string{"crash ls", "crash ls-new"}
	return runCephCommands(sc, commands)
}

type MonStatus struct{}

func (s *MonStatus) Name() string        { return "mon_status" }
func (s *MonStatus) Description() string  { return "检查 Monitor 仲裁状态和 leader 选举" }
func (s *MonStatus) Execute(sc *Context) (string, error) {
	commands := []string{"mon stat", "quorum_status"}
	return runCephCommands(sc, commands)
}

type IOStat struct{}

func (s *IOStat) Name() string        { return "io_stat" }
func (s *IOStat) Description() string  { return "查看节点磁盘 IO 统计" }
func (s *IOStat) Execute(sc *Context) (string, error) {
	if len(sc.Nodes) == 0 {
		return "没有可用的 SSH 节点来执行 IO 统计", nil
	}

	var results []string
	for _, node := range sc.Nodes {
		sshNode := config.SSHNode{
			Name:    node.Name,
			Host:    node.Host,
			User:    node.User,
			KeyFile: node.KeyFile,
		}
		output, err := sc.RunOnNode(sshNode, "iostat -x 1 3 2>/dev/null || cat /proc/diskstats")
		if err != nil {
			results = append(results, fmt.Sprintf("=== %s ===\nERROR: %v", node.Name, err))
		} else {
			results = append(results, fmt.Sprintf("=== %s ===\n%s", node.Name, output))
		}
	}
	return strings.Join(results, "\n\n"), nil
}

type ListNodes struct{}

func (s *ListNodes) Name() string        { return "list_nodes" }
func (s *ListNodes) Description() string  { return "获取集群所有节点信息（名称、IP、角色）" }
func (s *ListNodes) Execute(sc *Context) (string, error) {
	if sc.KubeExec == nil {
		return "", fmt.Errorf("no kubernetes connection available")
	}

	nodes, err := sc.KubeExec.DiscoverNodes(sc.Ctx)
	if err != nil {
		return "", fmt.Errorf("discover nodes: %w", err)
	}

	if len(nodes) == 0 {
		return "未发现节点", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("集群 %s 共 %d 个节点:\n\n", sc.ClusterName, len(nodes)))
	sb.WriteString(fmt.Sprintf("%-40s  %s\n", "NODE NAME", "INTERNAL IP"))
	sb.WriteString(strings.Repeat("-", 60) + "\n")
	for _, n := range nodes {
		sb.WriteString(fmt.Sprintf("%-40s  %s\n", n.Name, n.InternalIP))
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
			results = append(results, fmt.Sprintf("=== ceph %s ===\nERROR: %v", cmd, err))
		} else {
			results = append(results, fmt.Sprintf("=== ceph %s ===\n%s", cmd, output))
		}
	}
	return strings.Join(results, "\n\n"), nil
}
