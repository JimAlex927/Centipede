package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"centipede/internal/modules/docmost/domain"
	"github.com/jackc/pgx/v5"
)

type PageVerificationCreateInput struct {
	PageID       string
	WorkspaceID  string
	SpaceID      string
	Type         string
	Mode         *string
	PeriodAmount *int
	PeriodUnit   *string
	ExpiresAt    *time.Time
	CreatorID    string
	VerifierIDs  []string
}

type PageVerificationUpdateInput struct {
	PageID       string
	WorkspaceID  string
	CreatorID    string
	Mode         *string
	PeriodAmount *int
	PeriodUnit   *string
	ExpiresAt    *time.Time
	VerifierIDs  []string
}

type PageVerificationListInput struct {
	WorkspaceID string
	ViewerID    string
	ViewerAdmin bool
	SpaceIDs    []string
	VerifierID  string
	Type        string
	Cursor      string
	Limit       int
	Query       string
}

type pageVerificationRecord struct {
	ID           string
	Type         string
	Status       string
	Mode         *string
	PeriodAmount *int
	PeriodUnit   *string
	ExpiresAt    *time.Time
}

func (repository *Repository) PageVerificationInfo(ctx context.Context, pageID, workspaceID string) (domain.PageVerificationInfo, error) {
	var value domain.PageVerificationInfo
	var id, storedStatus, typ *string
	var verifiedBy, requestedBy, rejectedBy domain.PageVerificationUser
	var verifiedByID, requestedByID, rejectedByID *string
	var verifiedByName, requestedByName, rejectedByName, verifiedByEmail, requestedByEmail, rejectedByEmail *string
	var verifiedByAvatar, requestedByAvatar, rejectedByAvatar *string

	err := repository.db.QueryRow(ctx, `
SELECT pv.id::text, pv.page_id::text, pv.type, pv.mode, pv.period_amount,
       pv.period_unit, COALESCE(pv.status, 'none'), pv.verified_at,
       pv.verified_by_id::text, vu.name, vu.email, vu.avatar_url,
       pv.expires_at, pv.requested_at, pv.requested_by_id::text,
       ru.name, ru.email, ru.avatar_url, pv.rejected_at,
       pv.rejected_by_id::text, ju.name, ju.email, ju.avatar_url,
       pv.rejection_comment
FROM page_verifications pv
LEFT JOIN users vu ON vu.id = pv.verified_by_id
LEFT JOIN users ru ON ru.id = pv.requested_by_id
LEFT JOIN users ju ON ju.id = pv.rejected_by_id
WHERE pv.page_id = $1 AND pv.workspace_id = $2`, pageID, workspaceID).Scan(
		&id, &value.PageID, &typ, &value.Mode, &value.PeriodAmount,
		&value.PeriodUnit, &storedStatus, &value.VerifiedAt,
		&verifiedByID, &verifiedByName, &verifiedByEmail, &verifiedByAvatar,
		&value.ExpiresAt, &value.RequestedAt, &requestedByID,
		&requestedByName, &requestedByEmail, &requestedByAvatar,
		&value.RejectedAt, &rejectedByID, &rejectedByName, &rejectedByEmail,
		&rejectedByAvatar, &value.RejectionComment,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PageVerificationInfo{Status: "none"}, nil
	}
	if err != nil {
		return domain.PageVerificationInfo{}, err
	}

	value.ID = id
	value.Type = valueOrEmpty(typ)
	value.Status = effectiveVerificationStatus(valueOrEmpty(typ), valueOrEmpty(storedStatus), value.ExpiresAt)
	if verifiedByID != nil {
		verifiedBy = verificationUser(*verifiedByID, verifiedByName, verifiedByEmail, verifiedByAvatar)
		value.VerifiedBy = &verifiedBy
	}
	if requestedByID != nil {
		requestedBy = verificationUser(*requestedByID, requestedByName, requestedByEmail, requestedByAvatar)
		value.RequestedBy = &requestedBy
	}
	if rejectedByID != nil {
		rejectedBy = verificationUser(*rejectedByID, rejectedByName, rejectedByEmail, rejectedByAvatar)
		value.RejectedBy = &rejectedBy
	}

	rows, err := repository.db.Query(ctx, `
SELECT u.id::text, u.name, u.email, u.avatar_url
FROM page_verifiers pv
JOIN users u ON u.id = pv.user_id
WHERE pv.page_verification_id = $1 AND u.deleted_at IS NULL
ORDER BY pv.is_primary DESC, pv.created_at, u.id`, id)
	if err != nil {
		return domain.PageVerificationInfo{}, err
	}
	defer rows.Close()
	value.Verifiers = make([]domain.PageVerificationUser, 0)
	for rows.Next() {
		var user domain.PageVerificationUser
		if err := rows.Scan(&user.ID, &user.Name, &user.Email, &user.AvatarURL); err != nil {
			return domain.PageVerificationInfo{}, err
		}
		value.Verifiers = append(value.Verifiers, user)
	}
	if err := rows.Err(); err != nil {
		return domain.PageVerificationInfo{}, err
	}
	return value, nil
}

func (repository *Repository) pageVerificationRecord(ctx context.Context, pageID, workspaceID string) (pageVerificationRecord, error) {
	var record pageVerificationRecord
	err := repository.db.QueryRow(ctx, `
SELECT id::text, type, COALESCE(status, 'none'), mode, period_amount,
       period_unit, expires_at
FROM page_verifications
WHERE page_id = $1 AND workspace_id = $2`, pageID, workspaceID).Scan(
		&record.ID, &record.Type, &record.Status, &record.Mode,
		&record.PeriodAmount, &record.PeriodUnit, &record.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return pageVerificationRecord{}, ErrNotFound
	}
	return record, err
}

func (repository *Repository) CreatePageVerification(ctx context.Context, input PageVerificationCreateInput) error {
	if len(input.VerifierIDs) == 0 || len(input.VerifierIDs) > 10 {
		return fmt.Errorf("between 1 and 10 verifiers are required")
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	status := "draft"
	var verifiedAt *time.Time
	var verifiedBy *string
	if input.Type == "expiring" {
		now := time.Now().UTC()
		status, verifiedAt, verifiedBy = "verified", &now, &input.CreatorID
	}

	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
INSERT INTO page_verifications
 (id, page_id, workspace_id, space_id, type, status, mode, period_amount,
  period_unit, verified_at, verified_by_id, expires_at, creator_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, input.PageID, input.WorkspaceID, input.SpaceID, input.Type, status,
		input.Mode, input.PeriodAmount, input.PeriodUnit, verifiedAt, verifiedBy,
		input.ExpiresAt, input.CreatorID)
	if err != nil {
		return err
	}
	if err := insertPageVerifiers(ctx, tx, id, input.CreatorID, input.VerifierIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (repository *Repository) UpdatePageVerification(ctx context.Context, input PageVerificationUpdateInput) error {
	if len(input.VerifierIDs) == 0 || len(input.VerifierIDs) > 10 {
		return fmt.Errorf("between 1 and 10 verifiers are required")
	}
	record, err := repository.pageVerificationRecord(ctx, input.PageID, input.WorkspaceID)
	if err != nil {
		return err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
UPDATE page_verifications
SET mode = $3, period_amount = $4, period_unit = $5, expires_at = $6,
    updated_at = now()
WHERE id = $1 AND page_id = $2 AND workspace_id = $7`,
		record.ID, input.PageID, input.Mode, input.PeriodAmount, input.PeriodUnit,
		input.ExpiresAt, input.WorkspaceID)
	if err != nil {
		return err
	}
	if err := replacePageVerifiers(ctx, tx, record.ID, input.CreatorID, input.VerifierIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (repository *Repository) DeletePageVerification(ctx context.Context, pageID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `
DELETE FROM page_verifications WHERE page_id = $1 AND workspace_id = $2`, pageID, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) PageVerificationList(ctx context.Context, input PageVerificationListInput) (domain.Pagination[domain.PageVerificationListItem], error) {
	limit := normalizeLimit(input.Limit)
	spaceIDs := input.SpaceIDs
	if spaceIDs == nil {
		spaceIDs = []string{}
	}
	rows, err := repository.db.Query(ctx, `
SELECT pv.id::text, pv.page_id::text, pv.space_id::text, pv.type,
       CASE
         WHEN pv.type = 'expiring' AND pv.expires_at IS NOT NULL AND pv.expires_at <= now() THEN 'expired'
         WHEN pv.type = 'expiring' AND pv.expires_at IS NOT NULL AND pv.expires_at <= now() + interval '30 days' THEN 'expiring'
         ELSE COALESCE(pv.status, 'none')
       END,
       pv.mode, pv.period_amount, pv.period_unit, pv.verified_at, pv.expires_at,
       pv.created_at, p.title, p.slug_id, p.icon, s.name, s.slug,
       COALESCE(json_agg(json_build_object(
         'id', u.id::text, 'name', u.name, 'avatarUrl', u.avatar_url
       ) ORDER BY pvs.is_primary DESC, pvs.created_at) FILTER (WHERE u.id IS NOT NULL), '[]'::json)
FROM page_verifications pv
JOIN pages p ON p.id = pv.page_id AND p.deleted_at IS NULL
JOIN spaces s ON s.id = pv.space_id AND s.deleted_at IS NULL
LEFT JOIN page_verifiers pvs ON pvs.page_verification_id = pv.id
LEFT JOIN users u ON u.id = pvs.user_id AND u.deleted_at IS NULL
WHERE pv.workspace_id = $1
  AND (COALESCE(array_length($2::text[], 1), 0) = 0 OR pv.space_id::text = ANY($2::text[]))
  AND ($3 = '' OR EXISTS (SELECT 1 FROM page_verifiers f WHERE f.page_verification_id = pv.id AND f.user_id::text = $3))
  AND ($4 = '' OR pv.type = $4)
  AND ($5 = '' OR pv.id::text < $5)
  AND ($6 = '' OR p.title ILIKE '%' || $6 || '%')
  AND ($7 OR s.visibility = 'public'
       OR EXISTS (SELECT 1 FROM space_members sm WHERE sm.space_id = s.id AND sm.user_id = $8 AND sm.deleted_at IS NULL)
       OR EXISTS (SELECT 1 FROM space_members sm JOIN group_users gu ON gu.group_id = sm.group_id
                  WHERE sm.space_id = s.id AND gu.user_id = $8 AND sm.deleted_at IS NULL))
GROUP BY pv.id, p.id, s.id
ORDER BY pv.id DESC
LIMIT $9`, input.WorkspaceID, spaceIDs, input.VerifierID, input.Type, input.Cursor,
		input.Query, input.ViewerAdmin, input.ViewerID, limit+1)
	if err != nil {
		return domain.Pagination[domain.PageVerificationListItem]{}, err
	}
	defer rows.Close()
	items := make([]domain.PageVerificationListItem, 0, limit)
	for rows.Next() {
		var item domain.PageVerificationListItem
		var verifiersJSON []byte
		if err := rows.Scan(&item.ID, &item.PageID, &item.SpaceID, &item.Type,
			&item.Status, &item.Mode, &item.PeriodAmount, &item.PeriodUnit,
			&item.VerifiedAt, &item.ExpiresAt, &item.CreatedAt, &item.PageTitle,
			&item.PageSlugID, &item.PageIcon, &item.SpaceName, &item.SpaceSlug,
			&verifiersJSON); err != nil {
			return domain.Pagination[domain.PageVerificationListItem]{}, err
		}
		if len(verifiersJSON) > 0 {
			if err := json.Unmarshal(verifiersJSON, &item.Verifiers); err != nil {
				return domain.Pagination[domain.PageVerificationListItem]{}, err
			}
		}
		if item.Verifiers == nil {
			item.Verifiers = []domain.PageVerificationUser{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[domain.PageVerificationListItem]{}, err
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

func (repository *Repository) PageVerificationCanVerify(ctx context.Context, pageID, workspaceID, userID string) (bool, error) {
	var allowed bool
	err := repository.db.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM page_verifications pv
  JOIN page_verifiers pvs ON pvs.page_verification_id = pv.id
  JOIN users u ON u.id = pvs.user_id
  WHERE pv.page_id = $1 AND pv.workspace_id = $2 AND pvs.user_id = $3
    AND u.deleted_at IS NULL
)`, pageID, workspaceID, userID).Scan(&allowed)
	return allowed, err
}

func (repository *Repository) VerifyPage(ctx context.Context, pageID, workspaceID, userID string) error {
	record, err := repository.pageVerificationRecord(ctx, pageID, workspaceID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	status := "approved"
	expiresAt := record.ExpiresAt
	if record.Type == "expiring" {
		status = "verified"
		expiresAt = verificationExpiry(now, record.Mode, record.PeriodAmount, record.PeriodUnit, record.ExpiresAt)
	}
	_, err = repository.db.Exec(ctx, `
UPDATE page_verifications
SET status = $3, verified_at = $4, verified_by_id = $5, expires_at = $6,
    requested_at = NULL, requested_by_id = NULL, rejected_at = NULL,
    rejected_by_id = NULL, rejection_comment = NULL, updated_at = now()
WHERE page_id = $1 AND workspace_id = $2`, pageID, workspaceID, status, now,
		userID, expiresAt)
	return err
}

func (repository *Repository) SubmitPageVerification(ctx context.Context, pageID, workspaceID, userID string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE page_verifications
SET status = 'in_approval', requested_at = now(), requested_by_id = $3,
    rejected_at = NULL, rejected_by_id = NULL, rejection_comment = NULL,
    updated_at = now()
WHERE page_id = $1 AND workspace_id = $2 AND type = 'qms'
  AND COALESCE(status, 'draft') IN ('draft', 'approved')`, pageID, workspaceID, userID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrInvalidInput
	}
	return err
}

func (repository *Repository) RejectPageVerification(ctx context.Context, pageID, workspaceID, userID, comment string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE page_verifications
SET status = 'draft', rejected_at = now(), rejected_by_id = $3,
    rejection_comment = NULLIF($4, ''), updated_at = now()
WHERE page_id = $1 AND workspace_id = $2 AND type = 'qms'
  AND status = 'in_approval'`, pageID, workspaceID, userID, comment)
	if err == nil && result.RowsAffected() == 0 {
		return ErrInvalidInput
	}
	return err
}

func (repository *Repository) MarkPageVerificationObsolete(ctx context.Context, pageID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE page_verifications SET status = 'obsolete', updated_at = now()
WHERE page_id = $1 AND workspace_id = $2 AND type = 'qms' AND status = 'approved'`, pageID, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrInvalidInput
	}
	return err
}

func insertPageVerifiers(ctx context.Context, tx pgx.Tx, verificationID, addedByID string, userIDs []string) error {
	for index, userID := range userIDs {
		if _, err := tx.Exec(ctx, `
INSERT INTO page_verifiers (id, page_verification_id, user_id, is_primary, added_by_id)
VALUES ($1, $2, $3, $4, $5)`, mustUUID(), verificationID, userID, index == 0, addedByID); err != nil {
			return err
		}
	}
	return nil
}

func replacePageVerifiers(ctx context.Context, tx pgx.Tx, verificationID, addedByID string, userIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM page_verifiers WHERE page_verification_id = $1`, verificationID); err != nil {
		return err
	}
	return insertPageVerifiers(ctx, tx, verificationID, addedByID, userIDs)
}

func mustUUID() string {
	id, err := newUUID()
	if err != nil {
		panic(err)
	}
	return id
}

func verificationUser(id string, name, email, avatar *string) domain.PageVerificationUser {
	user := domain.PageVerificationUser{ID: id, Name: name, AvatarURL: avatar}
	if email != nil {
		user.Email = *email
	}
	return user
}

func effectiveVerificationStatus(typ, status string, expiresAt *time.Time) string {
	if typ == "expiring" && expiresAt != nil {
		now := time.Now().UTC()
		if !expiresAt.After(now) {
			return "expired"
		}
		if !expiresAt.After(now.AddDate(0, 0, 30)) {
			return "expiring"
		}
	}
	if status == "" {
		return "none"
	}
	return status
}

func verificationExpiry(now time.Time, mode *string, amount *int, unit *string, current *time.Time) *time.Time {
	if mode == nil || *mode == "indefinite" {
		return nil
	}
	if *mode == "fixed" {
		return current
	}
	if amount == nil || unit == nil {
		return current
	}
	days := *amount
	switch *unit {
	case "week":
		days *= 7
	case "month":
		days *= 30
	case "year":
		days *= 365
	}
	expires := now.AddDate(0, 0, days)
	return &expires
}
