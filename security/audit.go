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

// AuditLog stores audit entries in a fixed-size ring buffer.
// Memory usage is bounded by maxSize regardless of how many entries are recorded.
type AuditLog struct {
	mu      sync.Mutex
	buf     []AuditEntry
	head    int
	count   int
	maxSize int
}

func NewAuditLog(maxSize int) *AuditLog {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &AuditLog{
		buf:     make([]AuditEntry, maxSize),
		maxSize: maxSize,
	}
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

	if a.count < a.maxSize {
		a.buf[a.count] = entry
		a.count++
		return
	}

	// overwrite the oldest entry, clearing its string references first
	a.buf[a.head] = entry
	a.head = (a.head + 1) % a.maxSize
}

func (a *AuditLog) Recent(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	if n > a.count {
		n = a.count
	}
	if n <= 0 {
		return nil
	}

	result := make([]AuditEntry, n)

	// start position of the n most-recent entries
	var start int
	if a.count < a.maxSize {
		start = a.count - n
	} else {
		start = (a.head + a.count - n) % a.maxSize
	}

	for i := 0; i < n; i++ {
		result[i] = a.buf[(start+i)%a.maxSize]
	}
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
