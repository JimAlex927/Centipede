package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"centipede/internal/modules/docmost/domain"
	"github.com/jackc/pgx/v5"
)

type AuditActor struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatarUrl,omitempty"`
}

type AuditResource struct {
	ID     string  `json:"id"`
	Name   *string `json:"name"`
	Slug   *string `json:"slug,omitempty"`
	SlugID *string `json:"slugId,omitempty"`
}

type AuditLog struct {
	ID           string          `json:"id"`
	WorkspaceID  string          `json:"workspaceId"`
	ActorID      *string         `json:"actorId,omitempty"`
	ActorType    string          `json:"actorType"`
	Event        string          `json:"event"`
	ResourceType string          `json:"resourceType"`
	ResourceID   *string         `json:"resourceId,omitempty"`
	SpaceID      *string         `json:"spaceId,omitempty"`
	Changes      json.RawMessage `json:"changes,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	IPAddress    *string         `json:"ipAddress,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	Actor        *AuditActor     `json:"actor,omitempty"`
	Resource     *AuditResource  `json:"resource,omitempty"`
}

type AuditListInput struct {
	WorkspaceID  string
	Event        string
	ResourceType string
	ActorID      string
	SpaceID      string
	StartDate    *time.Time
	EndDate      *time.Time
	Cursor       string
	Limit        int
}

type AuditInput struct {
	WorkspaceID  string
	ActorID      *string
	ActorType    string
	Event        string
	ResourceType string
	ResourceID   *string
	SpaceID      *string
	Changes      json.RawMessage
	Metadata     json.RawMessage
	IPAddress    string
}

func (repository *Repository) AuditLogs(ctx context.Context, input AuditListInput) (domain.Pagination[AuditLog], error) {
	limit := normalizeLimit(input.Limit)
	rows, err := repository.db.Query(ctx, `
SELECT a.id::text, a.workspace_id::text, a.actor_id::text, a.actor_type,
       a.event, a.resource_type, a.resource_id::text, a.space_id::text,
       a.changes, a.metadata, a.ip_address::text, a.created_at,
       au.id::text, au.name, au.email, au.avatar_url,
       COALESCE(rp.id::text, rs.id::text, rg.id::text, ru.id::text),
       COALESCE(rp.title, rs.name, rg.name, ru.name),
       rs.slug, rp.slug_id
FROM audit a
LEFT JOIN users au ON au.id = a.actor_id
LEFT JOIN pages rp ON a.resource_type = 'page' AND rp.id = a.resource_id
LEFT JOIN spaces rs ON a.resource_type IN ('space', 'space_member') AND rs.id = a.resource_id
LEFT JOIN groups rg ON a.resource_type = 'group' AND rg.id = a.resource_id
LEFT JOIN users ru ON a.resource_type = 'user' AND ru.id = a.resource_id
WHERE a.workspace_id = $1
  AND ($2 = '' OR a.event = $2)
  AND ($3 = '' OR a.resource_type = $3)
  AND ($4 = '' OR a.actor_id::text = $4)
  AND ($5 = '' OR a.space_id::text = $5)
  AND ($6 = '' OR a.id::text < $6)
  AND ($7::timestamptz IS NULL OR a.created_at >= $7)
  AND ($8::timestamptz IS NULL OR a.created_at <= $8)
ORDER BY a.id DESC
LIMIT $9`, input.WorkspaceID, input.Event, input.ResourceType, input.ActorID, input.SpaceID, input.Cursor, input.StartDate, input.EndDate, limit+1)
	if err != nil {
		return domain.Pagination[AuditLog]{}, err
	}
	defer rows.Close()
	items := make([]AuditLog, 0, limit)
	for rows.Next() {
		var item AuditLog
		var actorID, actorName, actorEmail, actorAvatar *string
		var resourceID, resourceName, resourceSlug, resourceSlugID *string
		var changes, metadata []byte
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.ActorID, &item.ActorType, &item.Event, &item.ResourceType, &item.ResourceID, &item.SpaceID, &changes, &metadata, &item.IPAddress, &item.CreatedAt, &actorID, &actorName, &actorEmail, &actorAvatar, &resourceID, &resourceName, &resourceSlug, &resourceSlugID); err != nil {
			return domain.Pagination[AuditLog]{}, err
		}
		if len(changes) > 0 {
			item.Changes = json.RawMessage(changes)
		}
		if len(metadata) > 0 {
			item.Metadata = json.RawMessage(metadata)
		}
		if actorID != nil {
			item.Actor = &AuditActor{ID: *actorID, Name: actorName, Email: valueOrEmpty(actorEmail), AvatarURL: actorAvatar}
		}
		if resourceID != nil {
			item.Resource = &AuditResource{ID: *resourceID, Name: resourceName, Slug: resourceSlug, SlugID: resourceSlugID}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[AuditLog]{}, err
	}
	hasNext := len(items) > limit
	if hasNext {
		items = items[:limit]
	}
	result := page(items, limit)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = input.Cursor != ""
	if hasNext {
		next := items[len(items)-1].ID
		result.Meta.NextCursor = &next
	}
	return result, nil
}

func (repository *Repository) AuditRetentionDays(ctx context.Context, workspaceID string) (int, error) {
	var days int
	err := repository.db.QueryRow(ctx, `SELECT COALESCE(audit_retention_days, 365)::int FROM workspaces WHERE id = $1 AND deleted_at IS NULL`, workspaceID).Scan(&days)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return days, err
}

func (repository *Repository) UpdateAuditRetentionDays(ctx context.Context, workspaceID string, days int) error {
	result, err := repository.db.Exec(ctx, `UPDATE workspaces SET audit_retention_days = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, workspaceID, days)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = repository.db.Exec(ctx, `DELETE FROM audit WHERE workspace_id = $1 AND created_at < now() - ($2::text || ' days')::interval`, workspaceID, days)
	return err
}

func (repository *Repository) RecordAudit(ctx context.Context, input AuditInput) error {
	if input.ActorType == "" {
		input.ActorType = "user"
	}
	_, err := repository.db.Exec(ctx, `
INSERT INTO audit (workspace_id, actor_id, actor_type, event, resource_type, resource_id, space_id, changes, metadata, ip_address)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, NULLIF($10, '')::inet)`, input.WorkspaceID, input.ActorID, input.ActorType, input.Event, input.ResourceType, input.ResourceID, input.SpaceID, nullableJSON(input.Changes), nullableJSON(input.Metadata), input.IPAddress)
	return err
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
