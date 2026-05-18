package config

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type ReloadCallback func(cfg *Config)

type Watcher struct {
	path      string
	callbacks []ReloadCallback
	mu        sync.Mutex
	lastHash  [32]byte
}

func NewWatcher(path string) *Watcher {
	hash := fileHash(path)
	return &Watcher{
		path:     path,
		lastHash: hash,
	}
}

func (w *Watcher) OnReload(cb ReloadCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

func (w *Watcher) Start(ctx context.Context) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sighup:
			slog.Info("received SIGHUP, reloading config")
			w.reload()
		case <-ticker.C:
			if w.fileChanged() {
				slog.Info("config file changed on disk, reloading")
				w.reload()
			}
		}
	}
}

func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		slog.Error("failed to reload config", "error", err)
		return
	}

	w.mu.Lock()
	callbacks := make([]ReloadCallback, len(w.callbacks))
	copy(callbacks, w.callbacks)
	w.mu.Unlock()

	for _, cb := range callbacks {
		cb(cfg)
	}

	slog.Info("config reloaded successfully", "clusters", len(cfg.Clusters))
}

func (w *Watcher) fileChanged() bool {
	hash := fileHash(w.path)
	if hash != w.lastHash {
		w.lastHash = hash
		return true
	}
	return false
}

func fileHash(path string) [32]byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}
	}
	return sha256.Sum256(data)
}
