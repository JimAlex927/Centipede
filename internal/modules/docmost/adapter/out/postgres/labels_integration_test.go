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

func TestSearchPageFiltersMigratedSchema(t *testing.T) {
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
	creatorID := "00000000-0000-0000-0000-000000000001"
	items, err := repo.SearchPagesWithOptions(ctx,
		"00000000-0000-0000-0000-000000000002", "title%_",
		nil, 10, creatorID, false,
		PageSearchOptions{
			CreatorID: &creatorID,
			LabelIDs:  []string{"00000000-0000-0000-0000-000000000003"},
			TitleOnly: true,
			Offset:    5,
		},
	)
	if err != nil {
		t.Fatalf("search filters query failed against migrated schema: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("unexpected search results for nonexistent workspace: %d", len(items))
	}
}

func TestAIEmbeddingQueriesMigratedSchema(t *testing.T) {
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
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.ai_embeddings') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Skip("AI embedding migration is not applied")
	}
	repo := New(pool)
	if _, err := repo.ClaimAIEmbeddingJobs(ctx, 1); err != nil {
		t.Fatalf("claim embedding jobs failed: %v", err)
	}
	items, err := repo.SemanticSearch(ctx, "00000000-0000-0000-0000-000000000001", "test-model", []float32{1, 0}, nil, 5, "00000000-0000-0000-0000-000000000002", false)
	if err != nil {
		t.Fatalf("semantic search failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("unexpected semantic results: %d", len(items))
	}
}

func TestAIEmbeddingJobRequeuesAfterSourceChange(t *testing.T) {
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
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.ai_embedding_jobs') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Skip("AI embedding migration is not applied")
	}
	workspaceID := "00000000-0000-0000-0000-000000000011"
	pageID := "00000000-0000-0000-0000-000000000012"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ai_embedding_jobs WHERE workspace_id = $1`, workspaceID)
	})
	repo := New(pool)
	if err := repo.EnqueueAIPageEmbedding(ctx, workspaceID, pageID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimAIEmbeddingJobs(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnqueueAIPageEmbedding(ctx, workspaceID, pageID); err != nil {
		t.Fatal(err)
	}
	var count int
	var status string
	if err := pool.QueryRow(ctx, `SELECT count(*)::int, min(status) FROM ai_embedding_jobs WHERE workspace_id = $1 AND page_id = $2`, workspaceID, pageID).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "pending" {
		t.Fatalf("requeued job = count %d, status %q; want one pending job", count, status)
	}
}

func TestLabelWritesAtomic(t *testing.T) {
	url := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid configuration")
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Session-local fixtures leave existing application records untouched.
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE pages(id uuid PRIMARY KEY,workspace_id uuid,deleted_at timestamptz);
 CREATE TEMP TABLE labels(id uuid PRIMARY KEY,name text,type text,workspace_id uuid,created_at timestamptz DEFAULT now(),updated_at timestamptz DEFAULT now(),UNIQUE(workspace_id,type,name));
 CREATE TEMP TABLE page_labels(id uuid PRIMARY KEY,page_id uuid REFERENCES pages(id),label_id uuid REFERENCES labels(id),UNIQUE(page_id,label_id));
 INSERT INTO pages VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	repo := New(pool)
	pageID := "00000000-0000-0000-0000-000000000001"
	workspaceID := "00000000-0000-0000-0000-000000000002"
	attached, err := repo.AddPageLabels(ctx, pageID, workspaceID, []string{"first", "first"})
	if err != nil || len(attached) != 1 {
		t.Fatalf("deduplication: %v, %v", attached, err)
	}
	attached, err = repo.AddPageLabels(ctx, pageID, workspaceID, []string{"second"})
	if err != nil || len(attached) != 1 || attached[0].Name != "second" {
		t.Fatalf("return newly attached only: %v, %v", attached, err)
	}
	if _, err = repo.AddPageLabels(ctx, pageID, workspaceID, []string{"rolled-back", " "}); err == nil {
		t.Fatal("expected invalid input")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM labels`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("transaction left labels: %d", count)
	}
	if _, err = repo.AddPageLabels(ctx, pageID, pageID, []string{"wrong-workspace"}); err == nil {
		t.Fatal("expected workspace rejection")
	}
	if _, err = repo.AddPageLabels(ctx, pageID, workspaceID, []string{"first"}); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM page_labels`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("duplicate links: %d", count)
	}
}
