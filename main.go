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

	slog.Info("config loaded", "clusters", len(cfg.Clusters), "llm_provider", cfg.LLM.Provider, "llm_model", cfg.LLM.Model)

	llmProvider, err := analyzer.NewProvider(cfg.LLM)
	if err != nil {
		slog.Error("failed to create LLM provider", "error", err)
		os.Exit(1)
	}

	feishuClient := lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret)
	clusterMgr := cluster.NewManager(cfg.Clusters)
	sshExec := &executor.SSHExecutor{}
	az := analyzer.NewAnalyzer(llmProvider)

	handler := bot.NewHandler(feishuClient, clusterMgr, sshExec, az, llmProvider)

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			go func() {
				if err := handler.HandleMessage(context.Background(), event); err != nil {
					slog.Error("handle message failed", "error", err)
				}
			}()
			return nil
		})

	wsClient := larkws.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret,
		larkws.WithEventHandler(eventHandler),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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
