package executor

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/config"
	"golang.org/x/crypto/ssh"
)

type SSHExecutor struct{}

func (s *SSHExecutor) Run(ctx context.Context, node config.SSHNode, cmd string) (string, error) {
	client, err := s.dial(ctx, node)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create SSH session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return string(output), fmt.Errorf("command %q failed: %w, output: %s", cmd, err, string(output))
	}
	return string(output), nil
}

func (s *SSHExecutor) ReadLog(ctx context.Context, node config.SSHNode, path string, tailLines int) (string, error) {
	if tailLines <= 0 {
		tailLines = 200
	}
	cmd := fmt.Sprintf("tail -n %d %s 2>/dev/null || echo '[file not found: %s]'", tailLines, path, path)
	return s.Run(ctx, node, cmd)
}

func (s *SSHExecutor) NodeDiagnostics(ctx context.Context, node config.SSHNode) (string, error) {
	commands := []struct {
		label string
		cmd   string
	}{
		{"dmesg (last 50 lines)", "dmesg | tail -50"},
		{"disk usage", "df -h"},
		{"memory", "free -h"},
		{"ceph processes", "ps aux | grep -E 'ceph|rook' | grep -v grep"},
		{"network errors", "ip -s link | grep -A2 'errors'"},
		{"system load", "uptime"},
	}

	var results []string
	for _, c := range commands {
		output, err := s.Run(ctx, node, c.cmd)
		if err != nil {
			results = append(results, fmt.Sprintf("=== %s ===\nERROR: %v", c.label, err))
		} else {
			results = append(results, fmt.Sprintf("=== %s ===\n%s", c.label, strings.TrimSpace(output)))
		}
	}

	return strings.Join(results, "\n\n"), nil
}

func (s *SSHExecutor) CollectLogs(ctx context.Context, node config.SSHNode) (string, error) {
	logPaths := []string{
		"/var/log/messages",
		"/var/log/syslog",
	}

	rookLogCmd := "find /var/lib/rook/rook-ceph/log -name '*.log' -mmin -60 2>/dev/null | head -5"
	recentLogs, _ := s.Run(ctx, node, rookLogCmd)

	for _, logFile := range strings.Split(strings.TrimSpace(recentLogs), "\n") {
		if logFile != "" {
			logPaths = append(logPaths, logFile)
		}
	}

	var results []string
	for _, path := range logPaths {
		output, err := s.ReadLog(ctx, node, path, 100)
		if err != nil && strings.Contains(output, "[file not found") {
			continue
		}
		if output != "" && !strings.Contains(output, "[file not found") {
			results = append(results, fmt.Sprintf("=== %s (last 100 lines) ===\n%s", path, output))
		}
	}

	return strings.Join(results, "\n\n"), nil
}

func (s *SSHExecutor) dial(ctx context.Context, node config.SSHNode) (*ssh.Client, error) {
	keyData, err := os.ReadFile(node.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", node.KeyFile, err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: node.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}

	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.DialContext(ctx, "tcp", node.Host)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", node.Host, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, node.Host, sshConfig)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", node.Host, err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}
