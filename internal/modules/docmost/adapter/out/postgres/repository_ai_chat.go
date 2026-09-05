package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

// AIChat and AIChatMessage deliberately mirror the public shapes consumed by
// the enterprise client. Keeping them in the Docmost repository also means
// chat history survives a Node -> Go service switch without a data copy.
type AIChat struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	CreatorID   string    `json:"creatorId"`
	Title       *string   `json:"title"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type AIChatMessage struct {
	ID        string          `json:"id"`
	ChatID    string          `json:"chatId"`
	Role      string          `json:"role"`
	Content   *string         `json:"content"`
	ToolCalls json.RawMessage `json:"toolCalls"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt time.Time       `json:"createdAt"`
}

func scanAIChat(row rowScanner) (AIChat, error) {
	var item AIChat
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.CreatorID, &item.Title, &item.CreatedAt, &item.UpdatedAt)
	if err == pgx.ErrNoRows {
		return AIChat{}, ErrNotFound
	}
	return item, err
}

func scanAIChatMessage(row rowScanner) (AIChatMessage, error) {
	var item AIChatMessage
	var toolCalls, metadata []byte
	err := row.Scan(&item.ID, &item.ChatID, &item.Role, &item.Content, &toolCalls, &metadata, &item.CreatedAt)
	if err == pgx.ErrNoRows {
		return AIChatMessage{}, ErrNotFound
	}
	if toolCalls != nil {
		item.ToolCalls = json.RawMessage(toolCalls)
	}
	if metadata != nil {
		item.Metadata = json.RawMessage(metadata)
	}
	return item, err
}

const aiChatColumns = `c.id::text, c.workspace_id::text, c.creator_id::text,
c.title, c.created_at, c.updated_at`

func (repository *Repository) CreateAIChat(ctx context.Context, workspaceID, creatorID string) (AIChat, error) {
	id, err := newUUID()
	if err != nil {
		return AIChat{}, err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO ai_chats (id, workspace_id, creator_id)
VALUES ($1, $2, $3)`, id, workspaceID, creatorID)
	if err != nil {
		return AIChat{}, err
	}
	return repository.AIChatByID(ctx, id, workspaceID, creatorID)
}

func (repository *Repository) AIChatByID(ctx context.Context, chatID, workspaceID, creatorID string) (AIChat, error) {
	return scanAIChat(repository.db.QueryRow(ctx, `SELECT `+aiChatColumns+`
FROM ai_chats c
WHERE c.id = $1 AND c.workspace_id = $2 AND c.creator_id = $3 AND c.deleted_at IS NULL`, chatID, workspaceID, creatorID))
}

func (repository *Repository) AIChats(ctx context.Context, workspaceID, creatorID, cursor string, limit int) (domain.Pagination[AIChat], error) {
	pageSize := normalizeLimit(limit)
	args := []any{workspaceID, creatorID}
	whereCursor := ""
	if strings.TrimSpace(cursor) != "" {
		value, err := decodeAIChatCursor(cursor)
		if err != nil {
			return domain.Pagination[AIChat]{}, ErrInvalidInput
		}
		whereCursor = " AND c.id < $3"
		args = append(args, value)
	}
	args = append(args, pageSize+1)
	rows, err := repository.db.Query(ctx, `SELECT `+aiChatColumns+`
FROM ai_chats c
WHERE c.workspace_id = $1 AND c.creator_id = $2 AND c.deleted_at IS NULL`+whereCursor+`
		ORDER BY c.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return domain.Pagination[AIChat]{}, err
	}
	defer rows.Close()
	items := make([]AIChat, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanAIChat(rows)
		if scanErr != nil {
			return domain.Pagination[AIChat]{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[AIChat]{}, err
	}
	hasNext := len(items) > pageSize
	if hasNext {
		items = items[:pageSize]
	}
	result := page(items, pageSize)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = cursor != ""
	if hasNext && len(items) > 0 {
		next := encodeAIChatCursor(items[len(items)-1].ID)
		result.Meta.NextCursor = &next
	}
	return result, nil
}

func (repository *Repository) UpdateAIChat(ctx context.Context, chatID, workspaceID, creatorID, title string) (AIChat, error) {
	result, err := repository.db.Exec(ctx, `
UPDATE ai_chats SET title = NULLIF($4, ''), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND creator_id = $3 AND deleted_at IS NULL`, chatID, workspaceID, creatorID, strings.TrimSpace(title))
	if err != nil {
		return AIChat{}, err
	}
	if result.RowsAffected() == 0 {
		return AIChat{}, ErrNotFound
	}
	return repository.AIChatByID(ctx, chatID, workspaceID, creatorID)
}

func (repository *Repository) DeleteAIChat(ctx context.Context, chatID, workspaceID, creatorID string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE ai_chats SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND creator_id = $3 AND deleted_at IS NULL`, chatID, workspaceID, creatorID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) SearchAIChats(ctx context.Context, workspaceID, creatorID, query string) ([]AIChat, error) {
	rows, err := repository.db.Query(ctx, `SELECT `+aiChatColumns+`
FROM ai_chats c
WHERE c.workspace_id = $1 AND c.creator_id = $2 AND c.deleted_at IS NULL
  AND lower(COALESCE(c.title, '')) LIKE lower($3)
ORDER BY c.updated_at DESC, c.id DESC LIMIT 50`, workspaceID, creatorID, "%"+strings.TrimSpace(query)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AIChat, 0)
	for rows.Next() {
		item, scanErr := scanAIChat(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const aiChatMessageColumns = `m.id::text, m.chat_id::text, m.role, m.content,
m.tool_calls, m.metadata, m.created_at`

func (repository *Repository) AIChatMessages(ctx context.Context, chatID, workspaceID string) ([]AIChatMessage, error) {
	rows, err := repository.db.Query(ctx, `SELECT `+aiChatMessageColumns+`
FROM ai_chat_messages m
WHERE m.chat_id = $1 AND m.workspace_id = $2 AND m.deleted_at IS NULL
ORDER BY m.created_at ASC, m.id ASC`, chatID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AIChatMessage, 0)
	for rows.Next() {
		item, scanErr := scanAIChatMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) CreateAIChatMessage(ctx context.Context, chatID, workspaceID, userID, role string, content string, toolCalls, metadata json.RawMessage) (AIChatMessage, error) {
	id, err := newUUID()
	if err != nil {
		return AIChatMessage{}, err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO ai_chat_messages (id, chat_id, workspace_id, user_id, role, content, tool_calls, metadata)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7::jsonb, $8::jsonb)`, id, chatID, workspaceID, userID, role, content, nullableJSON(toolCalls), nullableJSON(metadata))
	if err != nil {
		return AIChatMessage{}, err
	}
	return scanAIChatMessage(repository.db.QueryRow(ctx, `SELECT `+aiChatMessageColumns+`
FROM ai_chat_messages m WHERE m.id = $1 AND m.workspace_id = $2`, id, workspaceID))
}

func (repository *Repository) ClaimAIChatAttachments(ctx context.Context, attachmentIDs []string, chatID, creatorID, workspaceID string) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	_, err := repository.db.Exec(ctx, `
UPDATE attachments SET ai_chat_id = $1, updated_at = now()
WHERE id::text = ANY($2) AND creator_id = $3 AND workspace_id = $4
  AND type = 'chat' AND ai_chat_id IS NULL AND deleted_at IS NULL`, chatID, attachmentIDs, creatorID, workspaceID)
	return err
}

func (repository *Repository) AIChatAttachments(ctx context.Context, chatID, workspaceID, creatorID string) ([]domain.Attachment, error) {
	rows, err := repository.db.Query(ctx, `SELECT `+attachmentColumns+`
FROM attachments a
JOIN ai_chats c ON c.id = a.ai_chat_id AND c.creator_id = $3 AND c.deleted_at IS NULL
WHERE a.ai_chat_id = $1 AND a.workspace_id = $2 AND a.type = 'chat' AND a.deleted_at IS NULL
ORDER BY a.created_at ASC`, chatID, workspaceID, creatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Attachment, 0)
	for rows.Next() {
		item, scanErr := scanAttachment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func encodeAIChatCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(url.Values{"id": []string{id}}.Encode()))
}

func decodeAIChatCursor(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	parsed, err := url.ParseQuery(string(decoded))
	if err != nil || !looksLikeUUID(parsed.Get("id")) {
		return "", ErrInvalidInput
	}
	return parsed.Get("id"), nil
}
