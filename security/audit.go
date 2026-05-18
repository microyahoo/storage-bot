package security

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type AuditEntry struct {
	Timestamp   time.Time
	User        string
	ClusterName string
	Action      string
	Command     string
	Status      string
}

type AuditLog struct {
	mu      sync.Mutex
	entries []AuditEntry
	maxSize int
}

func NewAuditLog(maxSize int) *AuditLog {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &AuditLog{maxSize: maxSize}
}

func (a *AuditLog) Record(user, clusterName, action, command, status string) {
	entry := AuditEntry{
		Timestamp:   time.Now(),
		User:        user,
		ClusterName: clusterName,
		Action:      action,
		Command:     command,
		Status:      status,
	}

	slog.Info("audit",
		"user", user,
		"cluster", clusterName,
		"action", action,
		"command", command,
		"status", status,
	)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.entries = append(a.entries, entry)
	if len(a.entries) > a.maxSize {
		a.entries = a.entries[len(a.entries)-a.maxSize:]
	}
}

func (a *AuditLog) Recent(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	if n > len(a.entries) {
		n = len(a.entries)
	}
	result := make([]AuditEntry, n)
	copy(result, a.entries[len(a.entries)-n:])
	return result
}

func (a *AuditLog) FormatRecent(n int) string {
	entries := a.Recent(n)
	if len(entries) == 0 {
		return "暂无操作记录"
	}

	var result string
	for _, e := range entries {
		result += fmt.Sprintf("[%s] user=%s cluster=%s action=%s status=%s\n",
			e.Timestamp.Format("15:04:05"), e.User, e.ClusterName, e.Action, e.Status)
	}
	return result
}
