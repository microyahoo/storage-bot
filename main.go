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

	handler := bot.NewHandler(feishuClient, clusterMgr, sshExec, az, llmProvider, skills, audit, cfg.Dev)

	// Register REST storage backends
	for name, restCfg := range cfg.RESTStorages {
		backend := storage.NewRESTBackend(name, restCfg.BaseURL, restCfg.APIKey, storage.RESTEndpoints{
			ClusterInfo: restCfg.Endpoints.ClusterInfo,
			DirUsage:    restCfg.Endpoints.DirUsage,
			HealthCheck: restCfg.Endpoints.HealthCheck,
		})
		handler.AddRESTStorage(name, storage.NewRESTSkill(name, backend))
		slog.Info("registered REST storage", "name", name, "base_url", restCfg.BaseURL)
	}

	// Config hot-reload: watch file changes + SIGHUP
	watcher := config.NewWatcher(*configPath)
	watcher.OnReload(func(newCfg *config.Config) {
		clusterMgr.Reload(newCfg.Clusters)
		handler.InvalidateKubeCache()
		slog.Info("clusters reloaded", "count", len(newCfg.Clusters))
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

	if cfg.Web.Listen != "" {
		webSrv, err := web.NewServer(cfg.Web, handler, handler)
		if err != nil {
			slog.Error("failed to init web server", "error", err)
			os.Exit(1)
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
