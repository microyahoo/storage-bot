package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/microyahoo/storage-bot/analyzer"
	"github.com/microyahoo/storage-bot/bot"
	"github.com/microyahoo/storage-bot/cluster"
	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
	"github.com/microyahoo/storage-bot/inspect"
	"github.com/microyahoo/storage-bot/security"
	"github.com/microyahoo/storage-bot/skill"
	"github.com/microyahoo/storage-bot/storage"
	"github.com/microyahoo/storage-bot/web"
)

func main() {
	configPath := flag.String("config", "config.yaml", "config file path")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("config loaded",
		"clusters", len(cfg.Clusters),
		"llm_provider", cfg.LLM.Provider,
		"llm_model", cfg.LLM.Model,
		"dev_disable_llm", cfg.Dev.DisableLLM,
		"dev_dry_run", cfg.Dev.DryRun,
	)

	var (
		llmProvider analyzer.LLMProvider
		az          *analyzer.Analyzer
	)
	llmProvider, err = analyzer.NewProvider(cfg.LLM)
	if err != nil {
		if !cfg.Dev.DisableLLM {
			slog.Error("failed to create LLM provider", "error", err)
			os.Exit(1)
		}
		slog.Warn("failed to create LLM provider, LLM features unavailable", "error", err)
	} else {
		az = analyzer.NewAnalyzer(llmProvider)
	}
	if cfg.Dev.DisableLLM {
		slog.Warn("dev.disable_llm = true, LLM disabled at startup (can be re-enabled via chat)")
	}

	feishuClient := lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret)
	clusterMgr := cluster.NewManager(cfg.Clusters)
	sshExec := &executor.SSHExecutor{}
	skills := skill.NewRegistry()
	audit := security.NewAuditLog(10000)

	// Cluster inspection (optional). Build runner+store first so the handler can
	// receive the runner for chat-triggered inspections.
	var (
		inspectRunner *inspect.Runner
		inspectStore  *inspect.Store
	)
	if cfg.Inspect.Enabled {
		inspectStore = inspect.NewStore(cfg.Inspect.HistoryDir, cfg.Inspect.HistoryKeep)
		inspectRunner = inspect.NewRunner(inspect.NewRegistry(), clusterMgr, sshExec, az,
			cfg.Inspect.Thresholds, cfg.Inspect.LLMSummary, inspectStore)
	}

	// webBase for report links is left empty: cfg.Web.Listen is a bind address
	// (e.g. ":8080"), not an externally reachable URL, so it would produce a
	// broken link. Cards omit the report link when webBase is empty.
	handler := bot.NewHandler(feishuClient, clusterMgr, sshExec,
		bot.WithAnalyzer(az),
		bot.WithLLM(llmProvider),
		bot.WithSkills(skills),
		bot.WithAudit(audit),
		bot.WithDev(cfg.Dev),
		bot.WithInspectRunner(inspectRunner, ""),
	)

	// Register REST storage backends (Yanrong only).
	handler.ReplaceRESTStorages(buildRESTStorages(cfg.RESTStorages))

	// Inspection scheduler (cron). handler implements inspect.Notifier so the
	// scheduler can push report cards to the configured chat.
	var inspectScheduler *inspect.Scheduler
	if cfg.Inspect.Enabled {
		inspectScheduler = inspect.NewScheduler(inspectRunner, cfg.Inspect, clusterMgr, handler, "")
	}

	// Config hot-reload: watch file changes + SIGHUP
	watcher := config.NewWatcher(*configPath)
	watcher.OnReload(func(newCfg *config.Config) {
		clusterMgr.Reload(newCfg.Clusters)
		handler.InvalidateKubeCache()
		handler.ReplaceRESTStorages(buildRESTStorages(newCfg.RESTStorages))
		if inspectScheduler != nil {
			inspectScheduler.Reload(newCfg.Inspect)
		}
		slog.Info("config reloaded",
			"clusters", len(newCfg.Clusters),
			"rest_storages", len(newCfg.RESTStorages),
		)
	})

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			go func() {
				if err := handler.HandleMessage(context.Background(), event); err != nil {
					slog.Error("handle message failed", "error", err)
				}
			}()
			return nil
		}).
		// Register no-op handlers for common events we don't process.
		// Without these the SDK logs [Error] "not found handler" for every reaction/read/recall.
		OnP2MessageReactionCreatedV1(func(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
			return nil
		}).
		OnP2MessageReactionDeletedV1(func(ctx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
			return nil
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			return nil
		}).
		OnP2MessageRecalledV1(func(ctx context.Context, event *larkim.P2MessageRecalledV1) error {
			return nil
		}).
		OnP2ChatMemberBotAddedV1(func(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
			slog.Info("bot added to chat")
			return nil
		}).
		OnP2ChatMemberBotDeletedV1(func(ctx context.Context, event *larkim.P2ChatMemberBotDeletedV1) error {
			slog.Info("bot removed from chat")
			return nil
		})

	wsClient := larkws.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret,
		larkws.WithEventHandler(eventHandler),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go watcher.Start(ctx)

	if inspectScheduler != nil {
		go inspectScheduler.Start(ctx)
		slog.Info("cluster inspection enabled", "schedule", cfg.Inspect.Schedule)
	}

	if cfg.Web.Listen != "" {
		webSrv, err := web.NewServer(cfg.Web, handler, handler)
		if err != nil {
			slog.Error("failed to init web server", "error", err)
			os.Exit(1)
		}
		if cfg.Inspect.Enabled {
			webSrv.SetInspect(inspectRunner, inspectStore)
		}
		go func() {
			if err := webSrv.Start(ctx); err != nil {
				slog.Error("web server error", "error", err)
			}
		}()
	}

	slog.Info("starting storage-bot, connecting to Feishu...")

	go func() {
		if err := wsClient.Start(ctx); err != nil {
			slog.Error("websocket client error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
}

// buildRESTStorages constructs a fresh storage skill map from config. Called
// at startup and again on every hot-reload so adds/removes/edits to
// rest_storages take effect without a restart.
func buildRESTStorages(cfgs map[string]*config.RESTStorageConfig) map[string]*storage.RESTSkill {
	out := make(map[string]*storage.RESTSkill, len(cfgs))
	for name, rc := range cfgs {
		backend := storage.NewYanrongBackend(name, rc.BaseURL, rc.Username, rc.Password,
			storage.WithUserPrefixes(rc.PublicUserPrefix, rc.PrivateUserPrefix))
		out[name] = storage.NewRESTSkill(name, backend)
		slog.Info("registered REST storage", "name", name, "type", backend.Type(), "base_url", rc.BaseURL)
	}
	return out
}
