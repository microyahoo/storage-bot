package executor

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/security"
	"golang.org/x/crypto/ssh"
)

type SSHExecutor struct{}

// HostIP strips the port from a "host:port" string, returning just the host.
// If there is no port, the input is returned as-is.
func HostIP(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// Run executes a command on node directly.
// For non-gateway nodes, use RunViaGateway instead.
func (s *SSHExecutor) Run(ctx context.Context, node config.SSHNode, cmd string) (string, error) {
	if err := security.ValidateSSHCommand(cmd); err != nil {
		return "", err
	}
	client, err := s.dial(ctx, node)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return s.runSession(client, cmd)
}

// RunViaGateway executes a command on target by tunneling through gateway.
// Use this when the bot only has direct SSH access to the gateway node,
// and the gateway has passwordless root SSH to other cluster nodes.
func (s *SSHExecutor) RunViaGateway(ctx context.Context, gateway, target config.SSHNode, cmd string) (string, error) {
	if err := security.ValidateSSHCommand(cmd); err != nil {
		return "", err
	}
	client, err := s.dialViaGateway(ctx, gateway, target)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return s.runSession(client, cmd)
}

func (s *SSHExecutor) ReadLog(ctx context.Context, node config.SSHNode, path string, tailLines int) (string, error) {
	if tailLines <= 0 {
		tailLines = 200
	}
	cmd := fmt.Sprintf("tail -n %d %s 2>/dev/null || echo '[file not found: %s]'", tailLines, path, path)
	return s.Run(ctx, node, cmd)
}

func (s *SSHExecutor) ReadLogViaGateway(ctx context.Context, gateway, target config.SSHNode, path string, tailLines int) (string, error) {
	if tailLines <= 0 {
		tailLines = 200
	}
	cmd := fmt.Sprintf("tail -n %d %s 2>/dev/null || echo '[file not found: %s]'", tailLines, path, path)
	return s.RunViaGateway(ctx, gateway, target, cmd)
}

func (s *SSHExecutor) NodeDiagnostics(ctx context.Context, node config.SSHNode) (string, error) {
	return s.nodeDiag(ctx, nil, node)
}

func (s *SSHExecutor) NodeDiagnosticsViaGateway(ctx context.Context, gateway, node config.SSHNode) (string, error) {
	return s.nodeDiag(ctx, &gateway, node)
}

func (s *SSHExecutor) nodeDiag(ctx context.Context, gateway *config.SSHNode, node config.SSHNode) (string, error) {
	commands := []struct {
		label string
		cmd   string
	}{
		{"dmesg (last 50 lines)", "dmesg | tail -50"},
		{"disk usage", "df -hT -x overlay -x tmpfs"},
		{"memory", "free -h"},
		{"ceph processes", "ps aux | grep -E 'ceph|rook' | grep -v grep"},
		{"network errors", "ip -s link | grep -A2 'errors'"},
		{"system load", "uptime"},
	}

	run := func(cmd string) (string, error) {
		if gateway != nil {
			return s.RunViaGateway(ctx, *gateway, node, cmd)
		}
		return s.Run(ctx, node, cmd)
	}

	var results []string
	for _, c := range commands {
		output, err := run(c.cmd)
		if err != nil {
			results = append(results, fmt.Sprintf("=== %s ===\nERROR: %v", c.label, err))
		} else {
			results = append(results, fmt.Sprintf("=== %s ===\n%s", c.label, strings.TrimSpace(output)))
		}
	}
	return strings.Join(results, "\n\n"), nil
}

func (s *SSHExecutor) CollectLogs(ctx context.Context, node config.SSHNode) (string, error) {
	return s.collectLogs(ctx, nil, node)
}

func (s *SSHExecutor) CollectLogsViaGateway(ctx context.Context, gateway, node config.SSHNode) (string, error) {
	return s.collectLogs(ctx, &gateway, node)
}

func (s *SSHExecutor) collectLogs(ctx context.Context, gateway *config.SSHNode, node config.SSHNode) (string, error) {
	run := func(cmd string) (string, error) {
		if gateway != nil {
			return s.RunViaGateway(ctx, *gateway, node, cmd)
		}
		return s.Run(ctx, node, cmd)
	}

	logPaths := []string{"/var/log/messages", "/var/log/syslog"}

	rookLogCmd := "find /var/lib/rook/rook-ceph/log -name '*.log' -mmin -60 2>/dev/null | head -5"
	recentLogs, _ := run(rookLogCmd)
	for _, logFile := range strings.Split(strings.TrimSpace(recentLogs), "\n") {
		if logFile != "" {
			logPaths = append(logPaths, logFile)
		}
	}

	var results []string
	for _, path := range logPaths {
		cmd := fmt.Sprintf("tail -n 100 %s 2>/dev/null || echo '[file not found: %s]'", path, path)
		output, err := run(cmd)
		if err != nil || strings.Contains(output, "[file not found") {
			continue
		}
		if output != "" {
			results = append(results, fmt.Sprintf("=== %s (last 100 lines) ===\n%s", path, output))
		}
	}
	return strings.Join(results, "\n\n"), nil
}

// dialViaGateway establishes a TCP tunnel through gateway, then does SSH handshake to target.
// Authentication for the first hop (bot → gateway) uses gateway.KeyFile on the bot's filesystem.
// Authentication for the second hop (gateway → target) uses the gateway's own private key,
// read via the already-established SSH session (~/.ssh/id_rsa on the gateway host).
func (s *SSHExecutor) dialViaGateway(ctx context.Context, gateway, target config.SSHNode) (*ssh.Client, error) {
	gwClient, err := s.dial(ctx, gateway)
	if err != nil {
		return nil, fmt.Errorf("dial gateway %s: %w", gateway.Host, err)
	}

	// Read the gateway's own private key through its SSH session.
	// The gateway uses this key for passwordless SSH to other cluster nodes.
	gwSigner, err := s.readRemotePrivateKey(gwClient, gateway.GatewayKeyFile)
	if err != nil {
		gwClient.Close()
		return nil, fmt.Errorf("read private key from gateway %s: %w", gateway.Host, err)
	}

	// Open a TCP tunnel through the gateway to the target node's SSH port.
	targetConn, err := gwClient.DialContext(ctx, "tcp", target.Host)
	if err != nil {
		gwClient.Close()
		return nil, fmt.Errorf("tunnel to %s via gateway: %w", target.Host, err)
	}

	targetCfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(gwSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	ncc, chans, reqs, err := ssh.NewClientConn(targetConn, target.Host, targetCfg)
	if err != nil {
		targetConn.Close()
		gwClient.Close()
		return nil, fmt.Errorf("SSH handshake with %s via gateway: %w", target.Host, err)
	}

	// Closing targetClient also closes gwClient.
	targetClient := ssh.NewClient(ncc, chans, reqs)
	go func() {
		targetClient.Wait()
		gwClient.Close()
	}()
	return targetClient, nil
}

func (s *SSHExecutor) runSession(client *ssh.Client, cmd string) (string, error) {
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

// readRemotePrivateKey reads the private key from the gateway over its existing SSH session.
// If gatewayKeyFile is set (a path on the remote host), that file is used directly.
// Otherwise, tries common default paths in order.
func (s *SSHExecutor) readRemotePrivateKey(gwClient *ssh.Client, gatewayKeyFile string) (ssh.Signer, error) {
	session, err := gwClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	defer session.Close()

	var cmd string
	if gatewayKeyFile != "" {
		cmd = fmt.Sprintf("cat %s", gatewayKeyFile)
	} else {
		cmd = "for f in ~/.ssh/id_rsa ~/.ssh/id_ed25519 ~/.ssh/id_ecdsa; do [ -f \"$f\" ] && cat \"$f\" && break; done"
	}

	keyData, err := session.CombinedOutput(cmd)
	if err != nil || len(keyData) == 0 {
		return nil, fmt.Errorf("no private key found on gateway (tried: %s)", cmd)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse gateway private key: %w", err)
	}
	return signer, nil
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
		User:            node.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
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

	ncc, chans, reqs, err := ssh.NewClientConn(conn, node.Host, sshConfig)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", node.Host, err)
	}
	return ssh.NewClient(ncc, chans, reqs), nil
}
