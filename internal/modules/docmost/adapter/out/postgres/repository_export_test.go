package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExportQueriesAgainstDatabase(t *testing.T) {
	databaseURL := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := New(pool)
	zeroID := "00000000-0000-0000-0000-000000000000"
	if _, err = repository.ExportPageTree(ctx, zeroID, zeroID, zeroID, false, false); err != nil {
		t.Fatalf("page export query failed: %v", err)
	}
	if _, err = repository.ExportSpacePages(ctx, zeroID, zeroID, zeroID, false); err != nil {
		t.Fatalf("space export query failed: %v", err)
	}
	if _, err = repository.AttachmentsByIDs(ctx, []string{"00000000-0000-0000-0000-000000000000"}, zeroID); err != nil {
		t.Fatalf("attachment export query failed: %v", err)
	}
}
