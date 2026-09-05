package postgres

import (
	"context"

	"centipede/internal/modules/docmost/domain"
)

type NotificationDelivery struct {
	ID     string
	UserID string
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
