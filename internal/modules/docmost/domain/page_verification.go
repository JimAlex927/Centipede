package domain

import "time"

type PageVerificationUser struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Email     string  `json:"email,omitempty"`
	AvatarURL *string `json:"avatarUrl"`
}

type PageVerificationInfo struct {
	ID               *string                `json:"id,omitempty"`
	PageID           *string                `json:"pageId,omitempty"`
	Type             string                 `json:"type,omitempty"`
	Mode             *string                `json:"mode,omitempty"`
	PeriodAmount     *int                   `json:"periodAmount,omitempty"`
	PeriodUnit       *string                `json:"periodUnit,omitempty"`
	Status           string                 `json:"status"`
	VerifiedAt       *time.Time             `json:"verifiedAt,omitempty"`
	VerifiedBy       *PageVerificationUser  `json:"verifiedBy,omitempty"`
	ExpiresAt        *time.Time             `json:"expiresAt,omitempty"`
	RequestedAt      *time.Time             `json:"requestedAt,omitempty"`
	RequestedBy      *PageVerificationUser  `json:"requestedBy,omitempty"`
	RejectedAt       *time.Time             `json:"rejectedAt,omitempty"`
	RejectedBy       *PageVerificationUser  `json:"rejectedBy,omitempty"`
	RejectionComment *string                `json:"rejectionComment,omitempty"`
	Verifiers        []PageVerificationUser `json:"verifiers,omitempty"`
}

type PageVerificationListItem struct {
	ID           string                 `json:"id"`
	PageID       string                 `json:"pageId"`
	SpaceID      string                 `json:"spaceId"`
	Type         string                 `json:"type"`
	Status       *string                `json:"status"`
	Mode         *string                `json:"mode"`
	PeriodAmount *int                   `json:"periodAmount"`
	PeriodUnit   *string                `json:"periodUnit"`
	VerifiedAt   *time.Time             `json:"verifiedAt"`
	ExpiresAt    *time.Time             `json:"expiresAt"`
	CreatedAt    time.Time              `json:"createdAt"`
	PageTitle    *string                `json:"pageTitle"`
	PageSlugID   string                 `json:"pageSlugId"`
	PageIcon     *string                `json:"pageIcon"`
	SpaceName    string                 `json:"spaceName"`
	SpaceSlug    string                 `json:"spaceSlug"`
	Verifiers    []PageVerificationUser `json:"verifiers"`
}
