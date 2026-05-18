package security

import (
	"fmt"
	"regexp"
	"strings"
)

var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-rf\b`),
	regexp.MustCompile(`(?i)\brm\s+-fr\b`),
	regexp.MustCompile(`(?i)\brm\s+(-[a-z]*f[a-z]*)\s+/`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\bdd\s+.*\bof=/dev/`),
	regexp.MustCompile(`(?i)\bformat\b`),
	regexp.MustCompile(`(?i)>\s*/dev/sd`),
	regexp.MustCompile(`(?i)\bshutdown\b`),
	regexp.MustCompile(`(?i)\breboot\b`),
	regexp.MustCompile(`(?i)\bhalt\b`),
	regexp.MustCompile(`(?i)\binit\s+0\b`),
	regexp.MustCompile(`(?i)\bpoweroff\b`),
	regexp.MustCompile(`(?i)\bsystemctl\s+(stop|disable|mask)\b`),
	regexp.MustCompile(`(?i)\bkill\s+-9\b`),
	regexp.MustCompile(`(?i)\bkillall\b`),
	regexp.MustCompile(`(?i)\bpkill\b`),
	regexp.MustCompile(`(?i)\biptables\s+-F\b`),
	regexp.MustCompile(`(?i)\biptables\s+.*-D\b`),
	regexp.MustCompile(`(?i)\buseradd\b`),
	regexp.MustCompile(`(?i)\buserdel\b`),
	regexp.MustCompile(`(?i)\bpasswd\b`),
	regexp.MustCompile(`(?i)\bchmod\s+777\b`),
	regexp.MustCompile(`(?i)\bchown\b.*\s+/`),
	regexp.MustCompile(`(?i)\bcurl\b.*\|\s*(ba)?sh\b`),
	regexp.MustCompile(`(?i)\bwget\b.*\|\s*(ba)?sh\b`),
	regexp.MustCompile(`(?i)\beval\b`),
	regexp.MustCompile(`(?i)\bexec\b.*</dev/tcp`),
	regexp.MustCompile(`(?i)\bnc\s+-[a-z]*l`),  // netcat listen
	regexp.MustCompile(`(?i)\bpython\b.*-c\b`),
	regexp.MustCompile(`(?i)\bperl\b.*-e\b`),
	regexp.MustCompile(`(?i)\bruby\b.*-e\b`),
	regexp.MustCompile(`(?i);\s*\b(rm|mkfs|dd|shutdown|reboot)\b`),       // chained dangerous commands
	regexp.MustCompile(`(?i)\|\s*\b(rm|mkfs|dd|shutdown|reboot|bash)\b`), // piped dangerous commands
	regexp.MustCompile(`(?i)\bceph\s+osd\s+(destroy|purge|rm)\b`),
	regexp.MustCompile(`(?i)\bceph\s+osd\s+pool\s+(delete|rm)\b`),
	regexp.MustCompile(`(?i)\bceph\s+mon\s+remove\b`),
	regexp.MustCompile(`(?i)\bceph\s+mds\s+fail\b`),
	regexp.MustCompile(`(?i)\bceph\s+fs\s+rm\b`),
	regexp.MustCompile(`(?i)\bceph\s+auth\s+del\b`),
	regexp.MustCompile(`(?i)\brbd\s+rm\b`),
	regexp.MustCompile(`(?i)\brados\s+rmpool\b`),
	regexp.MustCompile(`(?i)\brados\s+purge\b`),
}

var shellMetaChars = regexp.MustCompile("[`$;|&(){}\\[\\]!><]")

func ValidateCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("empty command")
	}

	for _, pat := range dangerousPatterns {
		if pat.MatchString(cmd) {
			return fmt.Errorf("command blocked by security policy: matches dangerous pattern %q", pat.String())
		}
	}

	return nil
}

func ValidateCephCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty ceph command")
	}

	full := strings.Join(args, " ")
	lower := strings.ToLower(full)

	readOnlyPrefixes := []string{
		"status", "health", "osd tree", "osd df", "osd stat", "osd status",
		"osd pool ls", "osd pool get", "osd pool stats",
		"df", "pg stat", "pg dump", "pg dump_stuck", "pg ls",
		"mon stat", "mon dump", "quorum_status",
		"fs ls", "fs get", "mds stat",
		"orch ls", "orch ps", "orch status",
		"crash ls", "crash ls-new", "crash info",
		"config show", "config get", "config dump",
		"version", "versions",
		"auth ls", "auth get",
		"tell", "daemon",
		"balancer status",
		"osd perf", "osd pool autoscale-status",
		"time-sync-status",
	}

	allowed := false
	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(lower, prefix) {
			allowed = true
			break
		}
	}

	if !allowed {
		return fmt.Errorf("ceph command %q is not in the read-only allow list", full)
	}

	return nil
}

func SanitizeForLLM(input string) string {
	input = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, input)

	const maxLen = 2000
	if len(input) > maxLen {
		input = input[:maxLen]
	}

	return input
}

func ValidateSSHCommand(cmd string) error {
	if err := ValidateCommand(cmd); err != nil {
		return err
	}

	if shellMetaChars.MatchString(cmd) {
		parts := strings.Fields(cmd)
		if len(parts) > 0 {
			safeCommands := map[string]bool{
				"tail": true, "head": true, "cat": true, "grep": true,
				"df": true, "free": true, "uptime": true, "ps": true,
				"dmesg": true, "journalctl": true, "iostat": true,
				"ip": true, "ss": true, "netstat": true,
				"find": true, "ls": true, "stat": true,
				"smartctl": true, "lsblk": true, "blkid": true,
				"top": true, "vmstat": true, "mpstat": true,
				"echo": true, "awk": true, "sed": true, "sort": true,
				"uniq": true, "wc": true, "tr": true, "cut": true,
				"ceph": true, "rbd": true, "rados": true,
			}
			if !safeCommands[parts[0]] {
				return fmt.Errorf("command %q uses shell metacharacters and is not in the safe command list", parts[0])
			}
		}
	}

	return nil
}
