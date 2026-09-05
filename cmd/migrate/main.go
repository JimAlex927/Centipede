package main

import (
	"context"
	"flag"
	"os"
	"time"

	"centipede/internal/platform/config"
	"centipede/internal/platform/database"
	"centipede/internal/platform/migrations"
	"centipede/pkg/logger"

	"go.uber.org/zap"
)

// NOTE: 迁移数据库需要修改config配置 也就是database的配置
// eg: postgres://allmacht:7777777@192.168.154.128:5433
func main() {
	directory := flag.String("dir", "migrations", "directory containing SQL migrations")
	adoptExisting := flag.Bool("adopt-existing", false, "adopt an already migrated Docmost database without executing the baseline SQL")
	checkExisting := flag.Bool("check-existing", false, "validate an existing Docmost database without changing it")
	flag.Parse()

	cleanup, err := logger.Init(logger.WithLevel("info"), logger.WithFileEnable(false))
	if err != nil {
		panic(err)
	}
	defer cleanup()
	if *adoptExisting && *checkExisting {
		logger.Error("-adopt-existing and -check-existing cannot be used together")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	databaseConfig, err := config.LoadDatabase(ctx)
	if err != nil {
		logger.Error("load configuration", zap.Error(err))
		os.Exit(1)
	}
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		logger.Error("open database", zap.Error(err))
		os.Exit(1)
	}
	defer pool.Close()

	var migrationErr error
	if *checkExisting {
		migrationErr = migrations.ValidateExisting(ctx, pool)
	} else if *adoptExisting {
		migrationErr = migrations.AdoptExisting(ctx, pool, *directory, "000001_docmost_baseline.sql")
	} else {
		migrationErr = migrations.Run(ctx, pool, *directory)
	}
	if migrationErr != nil {
		logger.Error("run migrations", zap.Error(migrationErr))
		os.Exit(1)
	}
	if *checkExisting {
		logger.Info("existing Docmost schema verified")
		return
	}
	logger.Info("database migrations applied")
}
