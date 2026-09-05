package domain

import (
	"encoding/json"
	"time"
)

type Workspace struct {
	ID                 string          `json:"id"`
	Name               *string         `json:"name"`
	Description        *string         `json:"description"`
	Logo               *string         `json:"logo"`
	Hostname           *string         `json:"hostname"`
	DefaultSpaceID     *string         `json:"defaultSpaceId"`
	CustomDomain       *string         `json:"customDomain"`
	Settings           json.RawMessage `json:"settings"`
	Status             *string         `json:"status"`
	EnforceSSO         bool            `json:"enforceSso"`
	EmailDomains       []string        `json:"emailDomains"`
	DefaultRole        string          `json:"defaultRole"`
	Plan               *string         `json:"plan"`
	EnforceMFA         bool            `json:"enforceMfa"`
	TrashRetentionDays int             `json:"trashRetentionDays"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
	MemberCount        int64           `json:"memberCount,omitempty"`
}

type User struct {
	ID                   string          `json:"id"`
	Name                 *string         `json:"name"`
	Email                string          `json:"email"`
	EmailVerifiedAt      *time.Time      `json:"emailVerifiedAt"`
	Password             *string         `json:"-"`
	AvatarURL            *string         `json:"avatarUrl"`
	Role                 *string         `json:"role"`
	WorkspaceID          *string         `json:"workspaceId"`
	Locale               *string         `json:"locale"`
	Timezone             *string         `json:"timezone"`
	Settings             json.RawMessage `json:"settings"`
	LastActiveAt         *time.Time      `json:"lastActiveAt"`
	LastLoginAt          *time.Time      `json:"lastLoginAt"`
	DeactivatedAt        *time.Time      `json:"deactivatedAt"`
	DeletedAt            *time.Time      `json:"deletedAt"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
	HasGeneratedPassword bool            `json:"hasGeneratedPassword"`
}

type Space struct {
	ID          string          `json:"id"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Slug        string          `json:"slug"`
	Logo        *string         `json:"logo"`
	Visibility  string          `json:"visibility"`
	DefaultRole string          `json:"defaultRole"`
	CreatorID   *string         `json:"creatorId"`
	WorkspaceID string          `json:"workspaceId"`
	Settings    json.RawMessage `json:"settings"`
	IsPersonal  bool            `json:"isPersonal"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	MemberCount int64           `json:"memberCount,omitempty"`
	Membership  *Membership     `json:"membership,omitempty"`
}

type Membership struct {
	UserID      string       `json:"userId"`
	Role        string       `json:"role"`
	Permissions []Permission `json:"permissions"`
}

// Permission is the CASL rule shape consumed by the Docmost frontend.
type Permission struct {
	Action  string `json:"action"`
	Subject string `json:"subject"`
}

type Page struct {
	ID              string          `json:"id"`
	SlugID          string          `json:"slugId"`
	Title           *string         `json:"title"`
	Icon            *string         `json:"icon"`
	CoverPhoto      *string         `json:"coverPhoto"`
	Position        *string         `json:"position"`
	Content         json.RawMessage `json:"content"`
	ParentPageID    *string         `json:"parentPageId"`
	CreatorID       *string         `json:"creatorId"`
	LastUpdatedByID *string         `json:"lastUpdatedById"`
	DeletedByID     *string         `json:"deletedById"`
	SpaceID         string          `json:"spaceId"`
	WorkspaceID     string          `json:"workspaceId"`
	IsLocked        bool            `json:"isLocked"`
	IsBase          bool            `json:"isBase"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	DeletedAt       *time.Time      `json:"deletedAt"`
	HasChildren     bool            `json:"hasChildren"`
	Creator         *UserSummary    `json:"creator,omitempty"`
	LastUpdatedBy   *UserSummary    `json:"lastUpdatedBy,omitempty"`
	DeletedBy       *UserSummary    `json:"deletedBy,omitempty"`
	Space           *SpaceSummary   `json:"space,omitempty"`
}

type PagePermissionMember struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"createdAt"`
	Type        string    `json:"type"`
	Email       string    `json:"email,omitempty"`
	AvatarURL   *string   `json:"avatarUrl,omitempty"`
	MemberCount int64     `json:"memberCount,omitempty"`
	IsDefault   bool      `json:"isDefault,omitempty"`
}

type UserSummary struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
}

type SpaceSummary struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Slug     string  `json:"slug"`
	UserRole *string `json:"userRole,omitempty"`
}

type Pagination[T any] struct {
	Items []T            `json:"items"`
	Meta  PaginationMeta `json:"meta"`
}

type PaginationMeta struct {
	Limit       int     `json:"limit"`
	HasNextPage bool    `json:"hasNextPage"`
	HasPrevPage bool    `json:"hasPrevPage"`
	NextCursor  *string `json:"nextCursor"`
	PrevCursor  *string `json:"prevCursor"`
}

type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	IsDefault   bool      `json:"isDefault"`
	IsExternal  bool      `json:"isExternal"`
	CreatorID   *string   `json:"creatorId"`
	WorkspaceID string    `json:"workspaceId"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	MemberCount int64     `json:"memberCount"`
}

type Comment struct {
	ID              string          `json:"id"`
	Content         json.RawMessage `json:"content"`
	Selection       *string         `json:"selection"`
	Type            *string         `json:"type"`
	CreatorID       *string         `json:"creatorId"`
	PageID          string          `json:"pageId"`
	ParentCommentID *string         `json:"parentCommentId"`
	ResolvedByID    *string         `json:"resolvedById"`
	ResolvedAt      *time.Time      `json:"resolvedAt"`
	WorkspaceID     string          `json:"workspaceId"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	EditedAt        *time.Time      `json:"editedAt"`
	DeletedAt       *time.Time      `json:"deletedAt"`
	Creator         *UserSummary    `json:"creator,omitempty"`
	ResolvedBy      *UserSummary    `json:"resolvedBy,omitempty"`
}

type Favorite struct {
	ID          string        `json:"id"`
	UserID      string        `json:"userId"`
	PageID      *string       `json:"pageId"`
	SpaceID     *string       `json:"spaceId"`
	TemplateID  *string       `json:"templateId"`
	Type        string        `json:"type"`
	WorkspaceID string        `json:"workspaceId"`
	CreatedAt   time.Time     `json:"createdAt"`
	Page        *PageSummary  `json:"page,omitempty"`
	Space       *SpaceSummary `json:"space,omitempty"`
}

type PageSummary struct {
	ID      string  `json:"id"`
	SlugID  string  `json:"slugId"`
	Title   *string `json:"title"`
	Icon    *string `json:"icon"`
	IsBase  bool    `json:"isBase"`
	SpaceID string  `json:"spaceId"`
}

type Session struct {
	ID              string    `json:"id"`
	DeviceName      *string   `json:"deviceName"`
	GeoLocation     *string   `json:"geoLocation"`
	LastActiveAt    time.Time `json:"lastActiveAt"`
	CreatedAt       time.Time `json:"createdAt"`
	IsCurrentDevice bool      `json:"isCurrentDevice"`
}

type SearchPage struct {
	ID           string        `json:"id"`
	Title        *string       `json:"title"`
	Icon         *string       `json:"icon"`
	ParentPageID *string       `json:"parentPageId"`
	SlugID       string        `json:"slugId"`
	CreatorID    *string       `json:"creatorId"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
	Rank         float32       `json:"rank"`
	Highlight    string        `json:"highlight"`
	Space        *SpaceSummary `json:"space"`
}

type AttachmentSearch struct {
	ID        string        `json:"id"`
	FileName  string        `json:"fileName"`
	PageID    string        `json:"pageId"`
	CreatorID *string       `json:"creatorId"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
	Rank      float32       `json:"rank"`
	Highlight string        `json:"highlight"`
	Space     *SpaceSummary `json:"space"`
	Page      *PageSummary  `json:"page"`
}

type Label struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	WorkspaceID string    `json:"workspaceId"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type SpaceMember struct {
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	Email       *string `json:"email,omitempty"`
	AvatarURL   *string `json:"avatarUrl,omitempty"`
	Type        string  `json:"type"`
	Role        string  `json:"role"`
	IsDefault   bool    `json:"isDefault,omitempty"`
	MemberCount int64   `json:"memberCount,omitempty"`
}

type Invitation struct {
	ID          string    `json:"id"`
	Token       string    `json:"-"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	InvitedByID *string   `json:"invitedById"`
	WorkspaceID string    `json:"workspaceId"`
	GroupIDs    []string  `json:"groupIds,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
	EnforceSSO  bool      `json:"enforceSso,omitempty"`
}

type BacklinkPage struct {
	ID        string        `json:"id"`
	SlugID    string        `json:"slugId"`
	Title     *string       `json:"title"`
	Icon      *string       `json:"icon"`
	SpaceID   string        `json:"spaceId"`
	UpdatedAt time.Time     `json:"updatedAt"`
	Space     *SpaceSummary `json:"space"`
}

type PageHistory struct {
	ID              string          `json:"id"`
	PageID          string          `json:"pageId"`
	SlugID          *string         `json:"slugId"`
	Title           *string         `json:"title"`
	Content         json.RawMessage `json:"content,omitempty"`
	Slug            *string         `json:"slug"`
	Icon            *string         `json:"icon"`
	CoverPhoto      *string         `json:"coverPhoto"`
	Version         *int32          `json:"version"`
	LastUpdatedByID *string         `json:"lastUpdatedById"`
	ContributorIDs  []string        `json:"contributorIds,omitempty"`
	SpaceID         string          `json:"spaceId"`
	WorkspaceID     string          `json:"workspaceId"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	LastUpdatedBy   *UserSummary    `json:"lastUpdatedBy,omitempty"`
	Contributors    []UserSummary   `json:"contributors,omitempty"`
}

type Share struct {
	ID              string        `json:"id"`
	Key             string        `json:"key"`
	PageID          string        `json:"pageId"`
	IncludeSubPages bool          `json:"includeSubPages"`
	SearchIndexing  bool          `json:"searchIndexing"`
	CreatorID       *string       `json:"creatorId"`
	SpaceID         string        `json:"spaceId"`
	WorkspaceID     string        `json:"workspaceId"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
	DeletedAt       *time.Time    `json:"deletedAt"`
	Level           int           `json:"level,omitempty"`
	SharedPage      *PageSummary  `json:"sharedPage,omitempty"`
	Page            *PageSummary  `json:"page,omitempty"`
	Space           *SpaceSummary `json:"space,omitempty"`
	Creator         *UserSummary  `json:"creator,omitempty"`
}

type Notification struct {
	ID          string          `json:"id"`
	UserID      string          `json:"userId"`
	WorkspaceID string          `json:"workspaceId"`
	Type        string          `json:"type"`
	ActorID     *string         `json:"actorId"`
	PageID      *string         `json:"pageId"`
	SpaceID     *string         `json:"spaceId"`
	CommentID   *string         `json:"commentId"`
	Data        json.RawMessage `json:"data"`
	ReadAt      *time.Time      `json:"readAt"`
	EmailedAt   *time.Time      `json:"emailedAt"`
	ArchivedAt  *time.Time      `json:"archivedAt"`
	CreatedAt   time.Time       `json:"createdAt"`
	Actor       *UserSummary    `json:"actor"`
	Page        *PageSummary    `json:"page"`
	Space       *SpaceSummary   `json:"space"`
}

type Attachment struct {
	ID          string       `json:"id"`
	FileName    string       `json:"fileName"`
	FilePath    string       `json:"filePath"`
	FileSize    int64        `json:"fileSize"`
	FileExt     string       `json:"fileExt"`
	MimeType    *string      `json:"mimeType"`
	Type        *string      `json:"type"`
	CreatorID   string       `json:"creatorId"`
	PageID      *string      `json:"pageId"`
	SpaceID     *string      `json:"spaceId"`
	WorkspaceID string       `json:"workspaceId"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	DeletedAt   *time.Time   `json:"deletedAt"`
	URL         string       `json:"url,omitempty"`
	Creator     *UserSummary `json:"creator,omitempty"`
}

type FileTask struct {
	ID           string          `json:"id"`
	Type         *string         `json:"type"`
	Source       *string         `json:"source"`
	Status       *string         `json:"status"`
	FileName     string          `json:"fileName"`
	FilePath     string          `json:"filePath"`
	FileSize     int64           `json:"fileSize"`
	FileExt      *string         `json:"fileExt"`
	ErrorMessage *string         `json:"errorMessage"`
	CreatorID    *string         `json:"creatorId"`
	PageID       *string         `json:"pageId"`
	SpaceID      *string         `json:"spaceId"`
	WorkspaceID  string          `json:"workspaceId"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	DeletedAt    *time.Time      `json:"deletedAt"`
}
