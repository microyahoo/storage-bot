package cluster

import (
	"testing"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

func TestKubeExecutorCaches(t *testing.T) {
	calls := 0
	m := NewManager(map[string]*config.ClusterConfig{
		"c1": {Kubeconfig: "x", Namespace: "rook-ceph"},
	})
	m.SetKubeExecFnForTest(func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
		calls++
		return &executor.KubeExecutor{}, nil
	})
	cfg, _ := m.Get("c1")
	if _, err := m.KubeExecutor("c1", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := m.KubeExecutor("c1", cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("kubeExecFn called %d times, want 1 (cached)", calls)
	}
}
