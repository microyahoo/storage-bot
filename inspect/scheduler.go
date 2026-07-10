package inspect

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/microyahoo/storage-bot/config"
	"github.com/robfig/cron/v3"
)

// Notifier sends a finished report somewhere (e.g. a Feishu chat). chatID may
// be empty if the scheduler is configured without a notify target.
type Notifier interface {
	NotifyReport(ctx context.Context, chatID string, rep *Report) error
	NotifySummary(ctx context.Context, chatID string, summary *Summary) error
}

// ClusterLister is the slice of cluster.Manager the scheduler needs to expand
// "all clusters".
type ClusterLister interface {
	List() []string
}

type Scheduler struct {
	runner   *Runner
	cfg      config.InspectConfig
	lister   ClusterLister
	notifier Notifier
	webBase  string

	mu      sync.Mutex
	cron    *cron.Cron
	entryID cron.EntryID
}

func NewScheduler(runner *Runner, cfg config.InspectConfig, lister ClusterLister, notifier Notifier, webBase string) *Scheduler {
	return &Scheduler{runner: runner, cfg: cfg, lister: lister, notifier: notifier, webBase: webBase}
}

func targetClusters(configured, all []string) []string {
	if len(configured) > 0 {
		return configured
	}
	return all
}

// Start registers the cron job and blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.install(s.cfg)
	<-ctx.Done()
	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
	}
	s.mu.Unlock()
}

// Reload rebuilds the cron entry from a new config (called on hot-reload).
func (s *Scheduler) Reload(cfg config.InspectConfig) {
	s.cfg = cfg
	s.install(cfg)
}

func (s *Scheduler) install(cfg config.InspectConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
	if !cfg.Enabled {
		s.cron = nil
		return
	}
	c := cron.New()
	id, err := c.AddFunc(cfg.Schedule, func() { s.tick(context.Background()) })
	if err != nil {
		slog.Error("inspect scheduler: bad schedule", "schedule", cfg.Schedule, "error", err)
		return
	}
	s.entryID = id
	s.cron = c
	c.Start()
	slog.Info("inspect scheduler installed", "schedule", cfg.Schedule)
}

// tick runs inspection for all target clusters, sends individual cards for
// abnormal ones, and a single summary card for the batch.
func (s *Scheduler) tick(ctx context.Context) {
	clusters := targetClusters(s.cfg.Clusters, s.lister.List())
	start := time.Now()

	var okNames, failedNames []string
	warnCount, critCount := 0, 0

	for _, name := range clusters {
		rep, err := s.runner.Run(ctx, name)
		if err != nil {
			slog.Error("inspect run failed", "cluster", name, "error", err)
			failedNames = append(failedNames, name)
			continue
		}
		switch rep.Overall {
		case LevelWarn:
			warnCount++
			s.notify(ctx, rep)
		case LevelCritical:
			critCount++
			s.notify(ctx, rep)
		default:
			okNames = append(okNames, name)
		}
	}

	sort.Strings(okNames)
	sort.Strings(failedNames)
	summary := &Summary{
		Total:       len(clusters),
		OK:          len(okNames),
		Warn:        warnCount,
		Critical:    critCount,
		Failed:      len(failedNames),
		OKNames:     okNames,
		FailedNames: failedNames,
		StartedAt:   start,
		Duration:    time.Since(start),
	}
	if s.notifier != nil && s.cfg.NotifyChat != "" {
		if err := s.notifier.NotifySummary(ctx, s.cfg.NotifyChat, summary); err != nil {
			slog.Error("inspect summary notify failed", "chat_id", s.cfg.NotifyChat, "error", err)
		}
	}
}

func (s *Scheduler) notify(ctx context.Context, rep *Report) {
	if s.notifier != nil && s.cfg.NotifyChat != "" {
		if err := s.notifier.NotifyReport(ctx, s.cfg.NotifyChat, rep); err != nil {
			slog.Error("inspect notify failed", "cluster", rep.Cluster, "chat_id", s.cfg.NotifyChat, "error", err)
		}
	}
}

// RunOnce runs a single cluster on demand (chat/web/API reuse this).
func (s *Scheduler) RunOnce(ctx context.Context, cluster string) (*Report, error) {
	return s.runner.Run(ctx, cluster)
}
