package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	docmostpostgres "centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/platform/config"
	"centipede/internal/platform/database"
	"centipede/internal/platform/httpapi"

	"go.uber.org/zap"
)

func Run(ctx context.Context, cfg config.Config, logger *zap.Logger) error {
	startupContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pool, err := database.Open(startupContext, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	docmostRepository := docmostpostgres.New(pool)
	go runMaintenance(ctx, docmostRepository, logger)

	server := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           httpapi.NewRouter(cfg, pool, logger),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	serverError := make(chan error, 1)
	go func() {
		logger.Info("centipede api listening", zap.String("address", cfg.Server.Address), zap.String("environment", cfg.Environment))
		serverError <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer shutdownCancel()
		return server.Shutdown(shutdownContext)
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func runMaintenance(ctx context.Context, repository *docmostpostgres.Repository, logger *zap.Logger) {
	cleanup := func() {
		maintenanceContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := repository.CleanupExpiredSessions(maintenanceContext); err != nil {
			logger.Warn("session cleanup failed", zap.Error(err))
			return
		}
		if err := repository.TrimExcessSessions(maintenanceContext); err != nil {
			logger.Warn("session retention cleanup failed", zap.Error(err))
			return
		}
		logger.Debug("session cleanup completed")
	}

	// Run once at startup so a server that was stopped for several days does
	// not wait another full interval before releasing stale sessions.
	cleanup()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
