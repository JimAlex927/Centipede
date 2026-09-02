package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"centipede/internal/app"
	"centipede/internal/platform/config"
	"centipede/pkg/banner"
	"centipede/pkg/logger"

	"go.uber.org/zap"
)

func main() {
	cleanup, err := logger.Init(
		logger.WithLevel("info"),
		logger.WithFileEnable(true),
		logger.WithFileDir("logs"),
		logger.WithFileBaseName("centipede-api"),
		logger.WithDailyRotate(true),
		logger.WithAlsoBySize(true),
		logger.WithSeparateLevel(false),
	)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	configRuntime, err := config.LoadRuntime(context.Background())
	if err != nil {
		logger.Error("load configuration", zap.Error(err))
		os.Exit(1)
	}
	defer func() {
		if err := configRuntime.Close(); err != nil {
			logger.Error("close configuration watcher", zap.Error(err))
		}
	}()
	cfg := configRuntime.Current()
	banner.Print("Centipede",
		banner.WithSubtitle("Modular reading vocabulary platform"),
		banner.WithVersion("dev"),
		banner.WithEnv(cfg.Environment),
		banner.WithPortText(cfg.Server.Address),
		banner.WithColor(true),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, logger.L()); err != nil {
		logger.Error("api stopped", zap.Error(err))
		os.Exit(1)
	}
}
