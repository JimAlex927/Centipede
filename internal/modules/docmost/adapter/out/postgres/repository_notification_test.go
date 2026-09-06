package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExtractCommentUserMentionIDs(t *testing.T) {
	content := []byte(`{
  "type":"doc",
  "content":[
    {"type":"paragraph","content":[
      {"type":"mention","attrs":{"entityType":"user","entityId":"user-1"}},
      {"type":"mention","attrs":{"entityType":"page","entityId":"page-1"}},
      {"type":"mention","attrs":{"entityType":"user","entityId":"user-1"}}
    ]},
    {"type":"blockquote","content":[
      {"type":"paragraph","content":[
        {"type":"mention","attrs":{"entityType":"user","entityId":"user-2"}}
      ]}
    ]}
  ]
}`)

	got := extractCommentUserMentionIDs(content)
	want := []string{"user-1", "user-2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExtractCommentUserMentionIDsInvalidJSON(t *testing.T) {
	if got := extractCommentUserMentionIDs([]byte(`not-json`)); got != nil {
		t.Fatalf("got %v for invalid JSON, want nil", got)
	}
}

func TestCommentNotificationsIntegration(t *testing.T) {
	databaseURL := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
CREATE TEMP TABLE pages(
  id uuid, parent_page_id uuid, workspace_id uuid, space_id uuid, deleted_at timestamptz
);
CREATE TEMP TABLE spaces(id uuid, deleted_at timestamptz, visibility text);
CREATE TEMP TABLE users(
  id uuid, workspace_id uuid, deleted_at timestamptz,
  deactivated_at timestamptz, settings jsonb, role text
);
CREATE TEMP TABLE comments(
  id uuid, creator_id uuid, parent_comment_id uuid,
  workspace_id uuid, deleted_at timestamptz
);
CREATE TEMP TABLE watchers(
  user_id uuid, workspace_id uuid, page_id uuid, type text, muted_at timestamptz
);
CREATE TEMP TABLE space_members(space_id uuid, user_id uuid, group_id uuid, deleted_at timestamptz);
CREATE TEMP TABLE group_users(group_id uuid, user_id uuid);
CREATE TEMP TABLE page_access(id uuid, page_id uuid);
CREATE TEMP TABLE page_permissions(id uuid, page_access_id uuid, user_id uuid, group_id uuid);
CREATE TEMP TABLE notifications(
  id uuid DEFAULT '00000000-0000-0000-0000-000000000999',
  user_id uuid, workspace_id uuid, type text, actor_id uuid,
  page_id uuid, space_id uuid, comment_id uuid, data jsonb,
  page_verification_id uuid
);
INSERT INTO pages VALUES(
  '00000000-0000-0000-0000-000000000901', NULL,
  '00000000-0000-0000-0000-000000000902',
  '00000000-0000-0000-0000-000000000903', NULL
);
INSERT INTO spaces VALUES(
  '00000000-0000-0000-0000-000000000903', NULL, 'public'
);
INSERT INTO users VALUES
  ('00000000-0000-0000-0000-000000000911', '00000000-0000-0000-0000-000000000902', NULL, NULL, '{}'::jsonb, 'member'),
  ('00000000-0000-0000-0000-000000000912', '00000000-0000-0000-0000-000000000902', NULL, NULL, '{}'::jsonb, 'member'),
  ('00000000-0000-0000-0000-000000000913', '00000000-0000-0000-0000-000000000902', NULL, NULL, '{}'::jsonb, 'member');
INSERT INTO watchers VALUES
  ('00000000-0000-0000-0000-000000000911', '00000000-0000-0000-0000-000000000902', '00000000-0000-0000-0000-000000000901', 'page', NULL),
  ('00000000-0000-0000-0000-000000000913', '00000000-0000-0000-0000-000000000902', '00000000-0000-0000-0000-000000000901', 'page', NULL);
INSERT INTO comments VALUES(
  '00000000-0000-0000-0000-000000000921',
  '00000000-0000-0000-0000-000000000912', NULL,
  '00000000-0000-0000-0000-000000000902', NULL
);
`)
	if err != nil {
		t.Fatal(err)
	}

	repository := New(pool)
	content, _ := json.Marshal(map[string]any{
		"type": "doc", "content": []any{map[string]any{
			"type": "mention", "attrs": map[string]any{
				"entityType": "user", "entityId": "00000000-0000-0000-0000-000000000911",
			},
		}},
	})
	comment := domain.Comment{
		ID: "00000000-0000-0000-0000-000000000921", Content: content,
		CreatorID:   stringPointer("00000000-0000-0000-0000-000000000912"),
		PageID:      "00000000-0000-0000-0000-000000000901",
		WorkspaceID: "00000000-0000-0000-0000-000000000902",
	}
	deliveries, err := repository.CreateCommentNotifications(ctx, comment, *comment.CreatorID, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 || deliveries[0].Type != "comment.user_mention" || deliveries[1].Type != "comment.created" {
		t.Fatalf("comment deliveries = %#v, want mention and watcher notifications", deliveries)
	}

	resolved, err := repository.CreateResolvedCommentNotification(ctx, comment, "00000000-0000-0000-0000-000000000913")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Type != "comment.resolved" || resolved.UserID != *comment.CreatorID {
		t.Fatalf("resolved delivery = %#v, want creator notification", resolved)
	}
}

func TestPageVerificationNotificationsIntegration(t *testing.T) {
	databaseURL := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
CREATE TEMP TABLE pages(id uuid, title text, parent_page_id uuid, workspace_id uuid, deleted_at timestamptz);
CREATE TEMP TABLE spaces(id uuid, deleted_at timestamptz, visibility text);
CREATE TEMP TABLE users(
  id uuid, workspace_id uuid, deleted_at timestamptz,
  deactivated_at timestamptz, settings jsonb, role text
);
CREATE TEMP TABLE page_verifications(
  id uuid, page_id uuid, workspace_id uuid, space_id uuid,
  creator_id uuid, requested_by_id uuid, type text, status text, expires_at timestamptz
);
CREATE TEMP TABLE page_verifiers(page_verification_id uuid, user_id uuid);
CREATE TEMP TABLE page_access(id uuid, page_id uuid);
CREATE TEMP TABLE page_permissions(id uuid, page_access_id uuid, user_id uuid, group_id uuid);
CREATE TEMP TABLE space_members(space_id uuid, user_id uuid, group_id uuid, deleted_at timestamptz);
CREATE TEMP TABLE group_users(group_id uuid, user_id uuid);
CREATE TEMP TABLE notifications(
  id uuid DEFAULT '00000000-0000-0000-0000-000000000999',
  user_id uuid, workspace_id uuid, type text, actor_id uuid,
  page_id uuid, space_id uuid, data jsonb, page_verification_id uuid
);
INSERT INTO pages VALUES(
  '00000000-0000-0000-0000-000000000901', 'Verification page', NULL,
  '00000000-0000-0000-0000-000000000902', NULL
);
INSERT INTO spaces VALUES(
  '00000000-0000-0000-0000-000000000903', NULL, 'public'
);
INSERT INTO users VALUES
  ('00000000-0000-0000-0000-000000000911', '00000000-0000-0000-0000-000000000902', NULL, NULL, '{}'::jsonb, 'member'),
  ('00000000-0000-0000-0000-000000000912', '00000000-0000-0000-0000-000000000902', NULL, NULL, '{}'::jsonb, 'member'),
  ('00000000-0000-0000-0000-000000000913', '00000000-0000-0000-0000-000000000902', NULL, NULL, '{}'::jsonb, 'member');
INSERT INTO space_members VALUES(
  '00000000-0000-0000-0000-000000000903',
  '00000000-0000-0000-0000-000000000912',
  NULL, NULL
);
INSERT INTO page_verifications VALUES(
  '00000000-0000-0000-0000-000000000931',
  '00000000-0000-0000-0000-000000000901',
  '00000000-0000-0000-0000-000000000902',
  '00000000-0000-0000-0000-000000000903',
  '00000000-0000-0000-0000-000000000911', NULL,
  'expiring', 'verified', now() + interval '5 days'
);
INSERT INTO page_verifiers VALUES(
  '00000000-0000-0000-0000-000000000931',
  '00000000-0000-0000-0000-000000000912'
);`)
	if err != nil {
		t.Fatal(err)
	}

	repository := New(pool)
	candidates, err := repository.PendingPageVerificationExpiryNotifications(ctx, time.Now().UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].NotificationType != "page.verification_expiring" {
		t.Fatalf("expiry candidates = %#v, want one expiring candidate", candidates)
	}
	eventKey := candidates[0].ExpiresAt.UTC().Format(time.RFC3339Nano)
	eventData, err := json.Marshal(map[string]string{"eventKey": eventKey})
	if err != nil {
		t.Fatal(err)
	}

	deliveries, err := repository.CreatePageVerificationNotifications(
		ctx,
		candidates[0].PageID,
		candidates[0].WorkspaceID,
		"00000000-0000-0000-0000-000000000913",
		candidates[0].NotificationType,
		eventKey,
		eventData,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].UserID != "00000000-0000-0000-0000-000000000912" {
		t.Fatalf("expiry deliveries = %#v, want the verifier only", deliveries)
	}
	again, err := repository.CreatePageVerificationNotifications(
		ctx, candidates[0].PageID, candidates[0].WorkspaceID,
		"00000000-0000-0000-0000-000000000913", candidates[0].NotificationType,
		eventKey,
		eventData,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("duplicate expiry deliveries = %#v, want none", again)
	}

	permissionDeliveries, err := repository.CreatePagePermissionNotifications(
		ctx,
		candidates[0].PageID,
		candidates[0].WorkspaceID,
		"00000000-0000-0000-0000-000000000903",
		"00000000-0000-0000-0000-000000000913",
		"writer",
		"permission-event",
		[]string{"00000000-0000-0000-0000-000000000912"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(permissionDeliveries) != 1 || permissionDeliveries[0].UserID != "00000000-0000-0000-0000-000000000912" {
		t.Fatalf("permission deliveries = %#v, want one target member", permissionDeliveries)
	}
}

func stringPointer(value string) *string {
	return &value
}
