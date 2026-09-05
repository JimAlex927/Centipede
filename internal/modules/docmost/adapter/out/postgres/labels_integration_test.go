package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

// Exercises the full query against the migrated schema without writing data.
func TestLabelQueriesMigratedSchema(t *testing.T) {
	url := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal("cannot configure test pool")
	}
	defer pool.Close()
	repo := New(pool)
	// A nonexistent workspace must return empty results even with admin access.
	workspace := "00000000-0000-0000-0000-000000000000"
	filter := LabelPageFilter{Name: "integration-schema-probe", Limit: 1}
	count, err := repo.LabelUsage(ctx, workspace, workspace, true, filter)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected count %d", count)
	}
	result, err := repo.PagesByLabel(ctx, workspace, workspace, true, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || result.Meta.HasNextPage {
		t.Fatal("unexpected label results")
	}
	labels, err := repo.Labels(ctx, workspace, workspace, true, "page", "probe", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels.Items) != 0 || labels.Meta.HasNextPage {
		t.Fatal("unexpected workspace labels")
	}
}
