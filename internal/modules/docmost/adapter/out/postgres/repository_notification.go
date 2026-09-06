package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

type NotificationDelivery struct {
	ID     string
	UserID string
}

type CommentNotificationDelivery struct {
	NotificationDelivery
	Type string
}

type PageVerificationExpiryCandidate struct {
	PageID           string
	WorkspaceID      string
	ExpiresAt        time.Time
	NotificationType string
}

// CreatePagePermissionNotifications creates notifications for users who were
// granted access to a page. A recipient must already have access to the page's
// space, matching Docmost's permission-granted worker behavior.
func (repository *Repository) CreatePagePermissionNotifications(ctx context.Context, pageID, workspaceID, spaceID, actorID, role, eventKey string, userIDs, groupIDs []string) ([]NotificationDelivery, error) {
	if role != "reader" && role != "writer" {
		return nil, ErrInvalidInput
	}
	if strings.TrimSpace(eventKey) == "" || len(userIDs) == 0 && len(groupIDs) == 0 {
		return nil, ErrInvalidInput
	}
	rows, err := repository.db.Query(ctx, `
WITH candidates AS (
  SELECT unnest($7::uuid[]) AS user_id
  UNION
  SELECT gu.user_id
  FROM group_users gu
  WHERE gu.group_id = ANY($8::uuid[])
), eligible AS (
  SELECT DISTINCT c.user_id
  FROM candidates c
  JOIN users u ON u.id = c.user_id
  WHERE u.workspace_id = $2
    AND u.deleted_at IS NULL AND u.deactivated_at IS NULL
	AND (EXISTS (
           SELECT 1 FROM space_members sm
           WHERE sm.space_id = $3 AND sm.user_id = c.user_id AND sm.deleted_at IS NULL
         ) OR EXISTS (
           SELECT 1 FROM space_members sm
           JOIN group_users member_gu ON member_gu.group_id = sm.group_id
           WHERE sm.space_id = $3 AND member_gu.user_id = c.user_id AND sm.deleted_at IS NULL
         ))
    AND NOT EXISTS (
      SELECT 1 FROM notifications n
      WHERE n.user_id = c.user_id AND n.workspace_id = $2
        AND n.page_id = $1 AND n.type = 'page.permission_granted'
        AND COALESCE(n.data->>'eventKey', '') = $6
    )
)
INSERT INTO notifications
  (user_id, workspace_id, type, actor_id, page_id, space_id, data)
SELECT e.user_id, $2, 'page.permission_granted', NULLIF($5, '')::uuid,
       $1, $3, jsonb_build_object('role', $4::text, 'eventKey', $6::text)
FROM eligible e
RETURNING id::text, user_id::text`, pageID, workspaceID, spaceID, role, actorID, eventKey, userIDs, groupIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]NotificationDelivery, 0)
	for rows.Next() {
		var delivery NotificationDelivery
		if err := rows.Scan(&delivery.ID, &delivery.UserID); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

// PendingPageVerificationExpiryNotifications returns verified expiring-page
// checks that need a reminder. The notification insert itself is idempotent,
// so this query can safely be retried by the hourly worker.
func (repository *Repository) PendingPageVerificationExpiryNotifications(ctx context.Context, now time.Time, limit int) ([]PageVerificationExpiryCandidate, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := repository.db.Query(ctx, `
SELECT pv.page_id::text, pv.workspace_id::text, pv.expires_at,
       CASE WHEN pv.expires_at <= $1 THEN 'page.verification_expired'
            ELSE 'page.verification_expiring' END
FROM page_verifications pv
JOIN pages p ON p.id = pv.page_id AND p.deleted_at IS NULL
WHERE pv.type = 'expiring'
  AND pv.status = 'verified'
  AND pv.expires_at IS NOT NULL
  AND pv.expires_at <= $1 + interval '30 days'
ORDER BY pv.expires_at ASC, pv.page_id ASC
LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]PageVerificationExpiryCandidate, 0, limit)
	for rows.Next() {
		var candidate PageVerificationExpiryCandidate
		if err := rows.Scan(&candidate.PageID, &candidate.WorkspaceID, &candidate.ExpiresAt, &candidate.NotificationType); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// CreatePageVerificationNotifications creates idempotent notifications for
// page verification workflow transitions. eventKey is the transition's
// timestamp (or another stable operation key), which prevents retries from
// duplicating the same notification.
func (repository *Repository) CreatePageVerificationNotifications(ctx context.Context, pageID, workspaceID, actorID, notificationType, eventKey string, data json.RawMessage) ([]NotificationDelivery, error) {
	switch notificationType {
	case "page.verified", "page.approval_requested", "page.approval_rejected", "page.verification_expiring", "page.verification_expired":
	default:
		return nil, ErrInvalidInput
	}
	if strings.TrimSpace(eventKey) == "" {
		return nil, ErrInvalidInput
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	rows, err := repository.db.Query(ctx, `
WITH verification AS (
  SELECT pv.id, pv.page_id, pv.space_id, pv.workspace_id,
         pv.creator_id, pv.requested_by_id
  FROM page_verifications pv
  WHERE pv.page_id = $1 AND pv.workspace_id = $2
), recipients AS (
  SELECT pvs.user_id
  FROM page_verifiers pvs
  JOIN verification v ON v.id = pvs.page_verification_id
  WHERE $4 IN ('page.verified', 'page.approval_requested',
               'page.verification_expiring', 'page.verification_expired')
  UNION
  SELECT v.requested_by_id
  FROM verification v
  WHERE $4 = 'page.approval_rejected' AND v.requested_by_id IS NOT NULL
), eligible AS (
  SELECT DISTINCT r.user_id, v.id AS verification_id, v.page_id, v.space_id,
         v.workspace_id
  FROM recipients r
  JOIN verification v ON true
  JOIN users u ON u.id = r.user_id
  JOIN pages p ON p.id = v.page_id AND p.deleted_at IS NULL
  JOIN spaces s ON s.id = v.space_id AND s.deleted_at IS NULL
  WHERE u.workspace_id = v.workspace_id
    AND u.deleted_at IS NULL AND u.deactivated_at IS NULL
	AND (EXISTS (SELECT 1 FROM space_members sm
				WHERE sm.space_id = s.id AND sm.user_id = u.id AND sm.deleted_at IS NULL)
	  OR EXISTS (SELECT 1 FROM space_members sm
				JOIN group_users gu ON gu.group_id = sm.group_id
				WHERE sm.space_id = s.id AND gu.user_id = u.id AND sm.deleted_at IS NULL))
    AND NOT EXISTS (
      WITH RECURSIVE ancestors AS (
        SELECT p0.id, p0.parent_page_id, ARRAY[p0.id] AS visited
        FROM pages p0
        WHERE p0.id = p.id AND p0.workspace_id = v.workspace_id AND p0.deleted_at IS NULL
        UNION ALL
        SELECT parent.id, parent.parent_page_id, a.visited || parent.id
        FROM pages parent JOIN ancestors a ON parent.id = a.parent_page_id
        WHERE parent.workspace_id = v.workspace_id
          AND parent.deleted_at IS NULL AND NOT parent.id = ANY(a.visited)
      )
      SELECT 1
      FROM ancestors a
      JOIN page_access pa ON pa.page_id = a.id
      WHERE NOT EXISTS (
        SELECT 1 FROM page_permissions pp
        WHERE pp.page_access_id = pa.id
          AND (pp.user_id = r.user_id OR pp.group_id IN (
            SELECT gu.group_id FROM group_users gu WHERE gu.user_id = r.user_id
          ))
      )
    )
    AND NOT EXISTS (
      SELECT 1 FROM notifications n
      WHERE n.user_id = r.user_id AND n.workspace_id = v.workspace_id
        AND n.page_verification_id = v.id AND n.type = $4
        AND COALESCE(n.data->>'eventKey', '') = $5
    )
)
INSERT INTO notifications
  (user_id, workspace_id, type, actor_id, page_id, space_id,
   data, page_verification_id)
SELECT e.user_id, e.workspace_id, $4, NULLIF($3, '')::uuid,
       e.page_id, e.space_id, $6::jsonb, e.verification_id
FROM eligible e
RETURNING id::text, user_id::text`, pageID, workspaceID, actorID, notificationType, eventKey, data)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]NotificationDelivery, 0)
	for rows.Next() {
		var delivery NotificationDelivery
		if err := rows.Scan(&delivery.ID, &delivery.UserID); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

type NotificationEmail struct {
	ID         string
	UserID     string
	Email      string
	Type       string
	Data       json.RawMessage
	ActorName  string
	PageTitle  string
	SpaceSlug  string
	PageSlugID string
}

type pageUserMention struct {
	UserID    string
	MentionID string
	CreatorID string
}

var notificationUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// CreatePageMentionNotifications creates direct in-app notifications for new
// user mentions found in the current page content. Mention IDs make the
// operation idempotent when collaboration persistence is retried.
func (repository *Repository) CreatePageMentionNotifications(ctx context.Context, pageID, workspaceID, actorID string, content []byte) ([]NotificationDelivery, error) {
	mentions := extractPageUserMentions(content, actorID)
	deliveries := make([]NotificationDelivery, 0, len(mentions))
	for _, mention := range mentions {
		if !notificationUUIDPattern.MatchString(mention.UserID) ||
			!notificationUUIDPattern.MatchString(mention.CreatorID) {
			continue
		}
		var delivery NotificationDelivery
		err := repository.db.QueryRow(ctx, `
INSERT INTO notifications (user_id, workspace_id, type, actor_id, page_id, space_id, data)
SELECT $1, p.workspace_id, 'page.user_mention', $3::uuid, p.id, p.space_id,
       jsonb_build_object('mentionId', $4::text)
FROM pages p
JOIN spaces s ON s.id = p.space_id AND s.deleted_at IS NULL
JOIN users u ON u.id = $1 AND u.workspace_id = p.workspace_id
WHERE p.id = $2 AND p.workspace_id = $5 AND p.deleted_at IS NULL
  AND u.deleted_at IS NULL AND u.deactivated_at IS NULL
  AND u.id <> $3::uuid
  AND COALESCE(u.settings->'notifications'->>'page.userMention', 'true') <> 'false'
  AND (s.visibility = 'public'
    OR EXISTS (SELECT 1 FROM space_members sm WHERE sm.space_id = s.id
               AND sm.user_id = u.id AND sm.deleted_at IS NULL)
    OR EXISTS (SELECT 1 FROM space_members sm JOIN group_users gu ON gu.group_id = sm.group_id
               WHERE sm.space_id = s.id AND gu.user_id = u.id AND sm.deleted_at IS NULL))
  AND NOT EXISTS (
    WITH RECURSIVE ancestors AS (
      SELECT id, parent_page_id, ARRAY[id] AS visited
      FROM pages WHERE id = p.id AND workspace_id = p.workspace_id AND deleted_at IS NULL
      UNION ALL
      SELECT parent.id, parent.parent_page_id, a.visited || parent.id
      FROM pages parent JOIN ancestors a ON parent.id = a.parent_page_id
      WHERE parent.workspace_id = p.workspace_id AND NOT parent.id = ANY(a.visited)
    )
    SELECT 1 FROM ancestors a
    JOIN page_access pa ON pa.page_id = a.id
    WHERE NOT EXISTS (
      SELECT 1 FROM page_permissions pp
      WHERE pp.page_access_id = pa.id
        AND (pp.user_id = u.id OR pp.group_id IN (
          SELECT gu.group_id FROM group_users gu WHERE gu.user_id = u.id
        ))
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM notifications n
    WHERE n.user_id = u.id AND n.workspace_id = p.workspace_id
      AND n.type = 'page.user_mention' AND n.page_id = p.id
      AND n.data->>'mentionId' = $4
  )
RETURNING id::text, user_id::text`, mention.UserID, pageID, mention.CreatorID, mention.MentionID, workspaceID).Scan(&delivery.ID, &delivery.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, nil
}

func extractPageUserMentions(content []byte, fallbackCreatorID string) []pageUserMention {
	var root any
	if json.Unmarshal(content, &root) != nil {
		return nil
	}
	result := make([]pageUserMention, 0)
	seen := make(map[string]struct{})
	var walk func(any)
	walk = func(value any) {
		node, ok := value.(map[string]any)
		if !ok {
			return
		}
		if node["type"] == "mention" {
			attrs, _ := node["attrs"].(map[string]any)
			entityType, _ := attrs["entityType"].(string)
			userID, _ := attrs["entityId"].(string)
			mentionID, _ := attrs["id"].(string)
			creatorID, _ := attrs["creatorId"].(string)
			if entityType == "user" && userID != "" && mentionID != "" {
				if creatorID == "" {
					creatorID = fallbackCreatorID
				}
				if _, exists := seen[mentionID]; !exists {
					seen[mentionID] = struct{}{}
					result = append(result, pageUserMention{UserID: userID, MentionID: mentionID, CreatorID: creatorID})
				}
			}
		}
		if children, ok := node["content"].([]any); ok {
			for _, child := range children {
				walk(child)
			}
		}
	}
	walk(root)
	return result
}

// CreateCommentNotifications mirrors Docmost's comment-notification worker.
// The operation is intentionally best-effort at the HTTP layer: a comment
// must remain saved even if notification delivery is temporarily unavailable.
func (repository *Repository) CreateCommentNotifications(ctx context.Context, comment domain.Comment, actorID string, oldMentionIDs []string, notifyWatchers bool) ([]CommentNotificationDelivery, error) {
	mentionedUserIDs := extractCommentUserMentionIDs(comment.Content)
	oldMentions := make(map[string]struct{}, len(oldMentionIDs))
	for _, id := range oldMentionIDs {
		oldMentions[id] = struct{}{}
	}
	newMentionIDs := make([]string, 0, len(mentionedUserIDs))
	for _, id := range mentionedUserIDs {
		if id == actorID {
			continue
		}
		if _, exists := oldMentions[id]; exists {
			continue
		}
		if !notificationUUIDPattern.MatchString(id) {
			continue
		}
		newMentionIDs = append(newMentionIDs, id)
	}

	if len(newMentionIDs) == 0 && !notifyWatchers && comment.ParentCommentID == nil {
		return nil, nil
	}

	recipientIDs := make([]string, 0)
	if comment.ParentCommentID != nil && notificationUUIDPattern.MatchString(*comment.ParentCommentID) {
		rows, err := repository.db.Query(ctx, `
SELECT DISTINCT creator_id::text
FROM comments
WHERE workspace_id = $1 AND deleted_at IS NULL
  AND (id = $2 OR parent_comment_id = $2)
  AND creator_id IS NOT NULL`, comment.WorkspaceID, *comment.ParentCommentID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			recipientIDs = append(recipientIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	} else if notifyWatchers {
		rows, err := repository.db.Query(ctx, `
SELECT DISTINCT user_id::text
FROM watchers
WHERE workspace_id = $1 AND page_id = $2 AND type = 'page' AND muted_at IS NULL`, comment.WorkspaceID, comment.PageID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			recipientIDs = append(recipientIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	deliveries := make([]CommentNotificationDelivery, 0, len(newMentionIDs)+len(recipientIDs))
	notified := map[string]struct{}{actorID: {}}
	for _, userID := range newMentionIDs {
		delivery, created, err := repository.insertCommentNotification(ctx, comment, actorID, userID, "comment.user_mention", "comment.userMention")
		if err != nil {
			return nil, err
		}
		if created {
			deliveries = append(deliveries, delivery)
			notified[userID] = struct{}{}
		}
	}
	for _, userID := range recipientIDs {
		if _, exists := notified[userID]; exists {
			continue
		}
		delivery, created, err := repository.insertCommentNotification(ctx, comment, actorID, userID, "comment.created", "comment.created")
		if err != nil {
			return nil, err
		}
		if created {
			deliveries = append(deliveries, delivery)
			notified[userID] = struct{}{}
		}
	}
	return deliveries, nil
}

func (repository *Repository) CreateResolvedCommentNotification(ctx context.Context, comment domain.Comment, actorID string) (*CommentNotificationDelivery, error) {
	if comment.CreatorID == nil || *comment.CreatorID == actorID || !notificationUUIDPattern.MatchString(*comment.CreatorID) {
		return nil, nil
	}
	delivery, created, err := repository.insertCommentNotification(ctx, comment, actorID, *comment.CreatorID, "comment.resolved", "comment.resolved")
	if err != nil || !created {
		return nil, err
	}
	return &delivery, nil
}

func (repository *Repository) insertCommentNotification(ctx context.Context, comment domain.Comment, actorID, userID, notificationType, settingKey string) (CommentNotificationDelivery, bool, error) {
	var delivery CommentNotificationDelivery
	delivery.Type = notificationType
	err := repository.db.QueryRow(ctx, `
INSERT INTO notifications (user_id, workspace_id, type, actor_id, page_id, space_id, comment_id)
SELECT u.id, p.workspace_id, $6, NULLIF($3, '')::uuid, p.id, p.space_id, $5
FROM pages p
JOIN spaces s ON s.id = p.space_id AND s.deleted_at IS NULL
JOIN users u ON u.id = $1 AND u.workspace_id = p.workspace_id
WHERE p.id = $2 AND p.workspace_id = $4 AND p.deleted_at IS NULL
  AND u.deleted_at IS NULL AND u.deactivated_at IS NULL AND u.id <> NULLIF($3, '')::uuid
  AND COALESCE(u.settings->'notifications'->>$7, 'true') <> 'false'
  AND (s.visibility = 'public'
    OR EXISTS (SELECT 1 FROM space_members sm WHERE sm.space_id = s.id
               AND sm.user_id = u.id AND sm.deleted_at IS NULL)
    OR EXISTS (SELECT 1 FROM space_members sm JOIN group_users gu ON gu.group_id = sm.group_id
               WHERE sm.space_id = s.id AND gu.user_id = u.id AND sm.deleted_at IS NULL))
  AND NOT EXISTS (
    WITH RECURSIVE ancestors AS (
      SELECT id, parent_page_id, ARRAY[id] AS visited
      FROM pages WHERE id = p.id AND workspace_id = p.workspace_id AND deleted_at IS NULL
      UNION ALL
      SELECT parent.id, parent.parent_page_id, a.visited || parent.id
      FROM pages parent JOIN ancestors a ON parent.id = a.parent_page_id
      WHERE parent.workspace_id = p.workspace_id AND NOT parent.id = ANY(a.visited)
    )
    SELECT 1 FROM ancestors a
    JOIN page_access pa ON pa.page_id = a.id
    WHERE NOT EXISTS (
      SELECT 1 FROM page_permissions pp
      WHERE pp.page_access_id = pa.id
        AND (pp.user_id = u.id OR pp.group_id IN (
          SELECT gu.group_id FROM group_users gu WHERE gu.user_id = u.id
        ))
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM notifications n
    WHERE n.user_id = u.id AND n.workspace_id = p.workspace_id
      AND n.type = $6 AND n.comment_id = $5
  )
RETURNING id::text, user_id::text`, userID, comment.PageID, actorID, comment.WorkspaceID, comment.ID, notificationType, settingKey).Scan(&delivery.ID, &delivery.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery, false, nil
	}
	if err != nil {
		return delivery, false, err
	}
	return delivery, true, nil
}

func extractCommentUserMentionIDs(content []byte) []string {
	var root any
	if json.Unmarshal(content, &root) != nil {
		return nil
	}
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	var walk func(any)
	walk = func(value any) {
		node, ok := value.(map[string]any)
		if !ok {
			return
		}
		if node["type"] == "mention" {
			attrs, _ := node["attrs"].(map[string]any)
			entityType, _ := attrs["entityType"].(string)
			userID, _ := attrs["entityId"].(string)
			if entityType == "user" && userID != "" {
				if _, exists := seen[userID]; !exists {
					seen[userID] = struct{}{}
					ids = append(ids, userID)
				}
			}
		}
		if children, ok := node["content"].([]any); ok {
			for _, child := range children {
				walk(child)
			}
		}
	}
	walk(root)
	return ids
}

// ExtractCommentUserMentionIDs exposes the same small JSON parser to the HTTP
// layer when it needs to compare mentions before and after an edit.
func ExtractCommentUserMentionIDs(content []byte) []string {
	return extractCommentUserMentionIDs(content)
}

func (repository *Repository) PendingCommentNotificationEmails(ctx context.Context, limit int) ([]NotificationEmail, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := repository.db.Query(ctx, `
SELECT n.id::text, n.user_id::text, u.email, n.type, n.data,
       COALESCE(actor.name, 'Someone'), COALESCE(p.title, 'a page'),
       COALESCE(s.slug, ''), COALESCE(p.slug_id, '')
FROM notifications n
JOIN users u ON u.id = n.user_id AND u.deleted_at IS NULL AND u.deactivated_at IS NULL
LEFT JOIN users actor ON actor.id = n.actor_id
LEFT JOIN pages p ON p.id = n.page_id
LEFT JOIN spaces s ON s.id = n.space_id
WHERE n.type IN (
	'comment.user_mention', 'comment.created', 'comment.resolved',
	'page.user_mention', 'page.permission_granted', 'page.updated',
	'page.approval_requested', 'page.approval_rejected',
    'page.verification_expiring', 'page.verification_expired'
  )
  AND n.emailed_at IS NULL
  AND n.created_at > now() - interval '7 days'
  AND u.email IS NOT NULL AND u.email <> ''
	AND (n.type IN ('page.permission_granted', 'page.approval_requested', 'page.approval_rejected',
				 'page.verification_expiring', 'page.verification_expired')
		OR (n.type = 'page.user_mention' AND COALESCE(u.settings->'notifications'->>'page.userMention', 'true') <> 'false')
		OR (n.type = 'page.updated' AND COALESCE(u.settings->'notifications'->>'page.updated', 'true') <> 'false')
    OR (n.type = 'comment.user_mention' AND COALESCE(u.settings->'notifications'->>'comment.userMention', 'true') <> 'false')
    OR (n.type = 'comment.created' AND COALESCE(u.settings->'notifications'->>'comment.created', 'true') <> 'false')
    OR (n.type = 'comment.resolved' AND COALESCE(u.settings->'notifications'->>'comment.resolved', 'true') <> 'false'))
ORDER BY n.created_at ASC, n.id ASC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NotificationEmail, 0, limit)
	for rows.Next() {
		var item NotificationEmail
		if err := rows.Scan(&item.ID, &item.UserID, &item.Email, &item.Type, &item.Data, &item.ActorName, &item.PageTitle, &item.SpaceSlug, &item.PageSlugID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) NotificationEmailByID(ctx context.Context, notificationID string) (*NotificationEmail, error) {
	var item NotificationEmail
	err := repository.db.QueryRow(ctx, `
SELECT n.id::text, n.user_id::text, u.email, n.type, n.data,
       COALESCE(actor.name, 'Someone'), COALESCE(p.title, 'a page'),
       COALESCE(s.slug, ''), COALESCE(p.slug_id, '')
FROM notifications n
JOIN users u ON u.id = n.user_id AND u.deleted_at IS NULL AND u.deactivated_at IS NULL
LEFT JOIN users actor ON actor.id = n.actor_id
LEFT JOIN pages p ON p.id = n.page_id
LEFT JOIN spaces s ON s.id = n.space_id
WHERE n.id = $1
  AND n.type IN (
	'comment.user_mention', 'comment.created', 'comment.resolved',
	'page.user_mention', 'page.permission_granted', 'page.updated',
	'page.approval_requested', 'page.approval_rejected',
    'page.verification_expiring', 'page.verification_expired'
  )
  AND n.emailed_at IS NULL
  AND u.email IS NOT NULL AND u.email <> ''
	AND (n.type IN ('page.permission_granted', 'page.approval_requested', 'page.approval_rejected',
				 'page.verification_expiring', 'page.verification_expired')
		OR (n.type = 'page.user_mention' AND COALESCE(u.settings->'notifications'->>'page.userMention', 'true') <> 'false')
		OR (n.type = 'page.updated' AND COALESCE(u.settings->'notifications'->>'page.updated', 'true') <> 'false')
    OR (n.type = 'comment.user_mention' AND COALESCE(u.settings->'notifications'->>'comment.userMention', 'true') <> 'false')
    OR (n.type = 'comment.created' AND COALESCE(u.settings->'notifications'->>'comment.created', 'true') <> 'false')
    OR (n.type = 'comment.resolved' AND COALESCE(u.settings->'notifications'->>'comment.resolved', 'true') <> 'false'))`, notificationID).Scan(
		&item.ID, &item.UserID, &item.Email, &item.Type, &item.Data, &item.ActorName, &item.PageTitle, &item.SpaceSlug, &item.PageSlugID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (repository *Repository) MarkNotificationEmailed(ctx context.Context, notificationID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE notifications SET emailed_at = now() WHERE id = $1 AND emailed_at IS NULL`, notificationID)
	return err
}

// CreatePageUpdateNotifications creates the local in-app notifications that
// Docmost normally produces in its notification worker. The seven-hour
// cooldown prevents a frequently edited page from flooding watchers.
func (repository *Repository) CreatePageUpdateNotifications(ctx context.Context, pageID, workspaceID, actorID string, actorIDs []string) ([]NotificationDelivery, error) {
	rows, err := repository.db.Query(ctx, `
WITH recipients AS (
  SELECT DISTINCT w.user_id, p.space_id
  FROM pages p
  JOIN watchers w ON w.workspace_id = p.workspace_id
    AND (w.page_id = p.id OR (w.page_id IS NULL AND w.space_id = p.space_id))
  JOIN spaces s ON s.id = p.space_id AND s.deleted_at IS NULL
  JOIN users u ON u.id = w.user_id AND u.workspace_id = p.workspace_id
  WHERE p.id = $1 AND p.workspace_id = $2 AND p.deleted_at IS NULL
    AND w.muted_at IS NULL AND u.deleted_at IS NULL AND u.deactivated_at IS NULL
    AND COALESCE(u.settings->'notifications'->>'page.updated', 'true') <> 'false'
    AND (s.visibility = 'public'
      OR EXISTS (SELECT 1 FROM space_members sm WHERE sm.space_id = s.id AND sm.user_id = u.id AND sm.deleted_at IS NULL)
      OR EXISTS (SELECT 1 FROM space_members sm JOIN group_users gu ON gu.group_id = sm.group_id
                 WHERE sm.space_id = s.id AND gu.user_id = u.id AND sm.deleted_at IS NULL))
    AND (COALESCE(array_length($4::uuid[], 1), 0) = 0 OR NOT (u.id = ANY($4::uuid[])))
), eligible AS (
  SELECT r.user_id, r.space_id
  FROM recipients r
  WHERE NOT EXISTS (
    SELECT 1 FROM notifications n
    WHERE n.user_id = r.user_id AND n.page_id = $1 AND n.workspace_id = $2
      AND n.type = 'page.updated' AND n.created_at > now() - interval '7 hours'
  )
)
INSERT INTO notifications (user_id, workspace_id, type, actor_id, page_id, space_id)
SELECT e.user_id, $2, 'page.updated', NULLIF($3, '')::uuid, $1, e.space_id
FROM eligible e
RETURNING id::text, user_id::text`, pageID, workspaceID, actorID, actorIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]NotificationDelivery, 0)
	for rows.Next() {
		var delivery NotificationDelivery
		if err := rows.Scan(&delivery.ID, &delivery.UserID); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (repository *Repository) Notifications(ctx context.Context, workspaceID, userID, notificationType string, limit int) (domain.Pagination[domain.Notification], error) {
	if notificationType == "" {
		notificationType = "all"
	}
	if notificationType != "all" && notificationType != "direct" && notificationType != "updates" {
		return domain.Pagination[domain.Notification]{}, ErrInvalidInput
	}
	rows, err := repository.db.Query(ctx, `
SELECT n.id::text, n.user_id::text, n.workspace_id::text, n.type,
 n.actor_id::text, n.page_id::text, n.space_id::text, n.comment_id::text,
 n.data, n.read_at, n.emailed_at, n.archived_at, n.created_at,
 actor.id::text, actor.name, actor.avatar_url,
 p.id::text, p.slug_id, p.title, p.icon, COALESCE(p.is_base, false), p.space_id::text,
 s.id::text, s.name, s.slug
FROM notifications n
LEFT JOIN users actor ON actor.id = n.actor_id
LEFT JOIN pages p ON p.id = n.page_id AND p.deleted_at IS NULL
LEFT JOIN spaces s ON s.id = n.space_id AND s.deleted_at IS NULL
WHERE n.user_id = $1 AND n.workspace_id = $2
  AND ($3 = 'all' OR ($3 = 'direct' AND n.type <> 'page.updated') OR ($3 = 'updates' AND n.type = 'page.updated'))
  AND (n.space_id IS NULL OR EXISTS (
    SELECT 1 FROM spaces permitted WHERE permitted.id = n.space_id AND
      (permitted.visibility = 'public'
       OR EXISTS (SELECT 1 FROM space_members sm WHERE sm.space_id = permitted.id AND sm.user_id = $1 AND sm.deleted_at IS NULL)
       OR EXISTS (SELECT 1 FROM space_members sm JOIN group_users gu ON gu.group_id = sm.group_id WHERE sm.space_id = permitted.id AND gu.user_id = $1 AND sm.deleted_at IS NULL))))
ORDER BY n.id DESC LIMIT $4`, userID, workspaceID, notificationType, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Notification]{}, err
	}
	defer rows.Close()
	items := make([]domain.Notification, 0)
	for rows.Next() {
		var item domain.Notification
		var actorID, actorName, actorAvatar *string
		var pageID, pageSlug, pageSpaceID *string
		var pageTitle, pageIcon *string
		var pageIsBase *bool
		var spaceID, spaceName, spaceSlug *string
		if err = rows.Scan(&item.ID, &item.UserID, &item.WorkspaceID, &item.Type,
			&item.ActorID, &item.PageID, &item.SpaceID, &item.CommentID, &item.Data,
			&item.ReadAt, &item.EmailedAt, &item.ArchivedAt, &item.CreatedAt,
			&actorID, &actorName, &actorAvatar,
			&pageID, &pageSlug, &pageTitle, &pageIcon, &pageIsBase, &pageSpaceID,
			&spaceID, &spaceName, &spaceSlug); err != nil {
			return domain.Pagination[domain.Notification]{}, err
		}
		if actorID != nil {
			item.Actor = &domain.UserSummary{ID: *actorID, Name: actorName, AvatarURL: actorAvatar}
		}
		if pageID != nil && pageSlug != nil && pageSpaceID != nil {
			item.Page = &domain.PageSummary{ID: *pageID, SlugID: *pageSlug, Title: pageTitle, Icon: pageIcon, IsBase: pageIsBase != nil && *pageIsBase, SpaceID: *pageSpaceID}
		}
		if spaceID != nil && spaceSlug != nil {
			item.Space = &domain.SpaceSummary{ID: *spaceID, Name: spaceName, Slug: *spaceSlug}
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) UnreadNotificationCount(ctx context.Context, workspaceID, userID string) (int64, error) {
	var count int64
	err := repository.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND workspace_id = $2 AND read_at IS NULL`, userID, workspaceID).Scan(&count)
	return count, err
}

func (repository *Repository) MarkNotificationsRead(ctx context.Context, workspaceID, userID string, notificationIDs []string) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	_, err := repository.db.Exec(ctx, `UPDATE notifications SET read_at = COALESCE(read_at, now()) WHERE user_id = $1 AND workspace_id = $2 AND id = ANY($3::uuid[])`, userID, workspaceID, notificationIDs)
	return err
}

func (repository *Repository) MarkAllNotificationsRead(ctx context.Context, workspaceID, userID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND workspace_id = $2 AND read_at IS NULL`, userID, workspaceID)
	return err
}
