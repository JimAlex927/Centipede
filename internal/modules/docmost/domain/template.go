package domain

import (
	"encoding/json"
	"time"
)

// Template is the workspace or space-scoped page blueprint exposed by the
// Docmost enterprise client.
type Template struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	Description     *string         `json:"description,omitempty"`
	Content         json.RawMessage `json:"content,omitempty"`
	Icon            *string         `json:"icon,omitempty"`
	SpaceID         *string         `json:"spaceId,omitempty"`
	WorkspaceID     string          `json:"workspaceId"`
	CreatorID       *string         `json:"creatorId,omitempty"`
	LastUpdatedByID *string         `json:"lastUpdatedById,omitempty"`
	Creator         *UserSummary    `json:"creator,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}
