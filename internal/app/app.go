package app

import (
	"context"
	"errors"
	"net/http"
	"time"

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
