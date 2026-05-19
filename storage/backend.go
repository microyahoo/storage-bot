package storage

import (
	"context"
)

// Backend is the common interface for any storage system the bot can query.
// Ceph clusters implement it via the KubeExecutor; other systems via REST API.
type Backend interface {
	// Type returns a human-readable label, e.g. "ceph" or "rest".
	Type() string
	// ClusterInfo returns a summary of the storage system (capacity, status, etc.).
	ClusterInfo(ctx context.Context) (string, error)
	// DirUsage returns disk usage for the specified path on the storage system.
	DirUsage(ctx context.Context, path string) (string, error)
	// HealthCheck returns a brief health status string.
	HealthCheck(ctx context.Context) (string, error)
}
