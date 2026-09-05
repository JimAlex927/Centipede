package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

const invitationColumns = `
id::text, token, COALESCE(email, ''), role, invited_by_id::text, workspace_id::text,
COALESCE(group_ids, '{}'::uuid[]), created_at, updated_at`

func scanInvitation(row rowScanner) (domain.Invitation, error) {
	var invitation domain.Invitation
	err := row.Scan(&invitation.ID, &invitation.Token, &invitation.Email, &invitation.Role,
		&invitation.InvitedByID, &invitation.WorkspaceID, &invitation.GroupIDs,
		&invitation.CreatedAt, &invitation.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Invitation{}, ErrNotFound
	}
	return invitation, err
}

func (repository *Repository) Invitations(ctx context.Context, workspaceID, query string, limit int) (domain.Pagination[domain.Invitation], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+invitationColumns+`
FROM workspace_invitations
WHERE workspace_id = $1 AND ($2 = '' OR email ILIKE '%' || $2 || '%')
ORDER BY id LIMIT $3`, workspaceID, strings.TrimSpace(query), normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Invitation]{}, err
	}
	defer rows.Close()
	items := make([]domain.Invitation, 0)
	for rows.Next() {
		item, scanErr := scanInvitation(rows)
		if scanErr != nil {
			return domain.Pagination[domain.Invitation]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) InvitationByID(ctx context.Context, invitationID, workspaceID string) (domain.Invitation, error) {
	return scanInvitation(repository.db.QueryRow(ctx, `SELECT `+invitationColumns+` FROM workspace_invitations WHERE id = $1 AND workspace_id = $2`, invitationID, workspaceID))
}

func (repository *Repository) InvitationToken(ctx context.Context, invitationID, workspaceID string) (string, error) {
	var token string
	err := repository.db.QueryRow(ctx, `SELECT token FROM workspace_invitations WHERE id = $1 AND workspace_id = $2`, invitationID, workspaceID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return token, err
}

func (repository *Repository) CreateInvitations(ctx context.Context, workspaceID, invitedByID, role string, emails, groupIDs []string) ([]domain.Invitation, error) {
	if role != "admin" && role != "member" {
		return nil, ErrInvalidInput
	}
	if len(emails) == 0 || len(emails) > 50 || len(groupIDs) > 25 {
		return nil, ErrInvalidInput
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	validGroups := make([]string, 0, len(groupIDs))
	if len(groupIDs) > 0 {
		rows, queryErr := tx.Query(ctx, `SELECT id::text FROM groups WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL`, workspaceID, groupIDs)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				rows.Close()
				return nil, scanErr
			}
			validGroups = append(validGroups, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}

	created := make([]domain.Invitation, 0, len(emails))
	seen := make(map[string]struct{}, len(emails))
	for _, rawEmail := range emails {
		email := strings.ToLower(strings.TrimSpace(rawEmail))
		if email == "" || !strings.Contains(email, "@") {
			return nil, ErrInvalidInput
		}
		if _, duplicate := seen[email]; duplicate {
			continue
		}
		seen[email] = struct{}{}
		token, tokenErr := randomID(24)
		if tokenErr != nil {
			return nil, tokenErr
		}
		invitation, insertErr := scanInvitation(tx.QueryRow(ctx, `
INSERT INTO workspace_invitations (email, role, token, group_ids, invited_by_id, workspace_id)
SELECT $1, $2, $3, $4::uuid[], $5, $6
WHERE NOT EXISTS (SELECT 1 FROM users WHERE workspace_id = $6 AND lower(email) = $1 AND deleted_at IS NULL)
ON CONFLICT (email, workspace_id) DO NOTHING
RETURNING `+invitationColumns, email, role, token, validGroups, invitedByID, workspaceID))
		if errors.Is(insertErr, ErrNotFound) {
			continue
		}
		if insertErr != nil {
			return nil, insertErr
		}
		created = append(created, invitation)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func (repository *Repository) RevokeInvitation(ctx context.Context, invitationID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `DELETE FROM workspace_invitations WHERE id = $1 AND workspace_id = $2`, invitationID, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) AcceptInvitation(ctx context.Context, invitationID, token, name, passwordHash, workspaceID string) (domain.User, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var email, role string
	var groupIDs []string
	var invitedByID *string
	err = tx.QueryRow(ctx, `SELECT COALESCE(email, ''), role, COALESCE(group_ids, '{}'::uuid[]), invited_by_id::text
FROM workspace_invitations WHERE id = $1 AND workspace_id = $2 AND token = $3 FOR UPDATE`, invitationID, workspaceID, token).Scan(&email, &role, &groupIDs, &invitedByID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	userID, err := newUUID()
	if err != nil {
		return domain.User{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO users
(id, name, email, email_verified_at, password, role, invited_by_id, workspace_id, settings, has_generated_password)
VALUES ($1, $2, lower($3), now(), $4, $5, $6, $7, '{"preferences":{"fullPageWidth":false,"pageEditMode":"edit","editorToolbar":true}}'::jsonb, false)`,
		userID, name, email, passwordHash, role, invitedByID, workspaceID)
	if err != nil {
		return domain.User{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO group_users (user_id, group_id)
SELECT $1, id FROM groups WHERE workspace_id = $2 AND is_default = true AND deleted_at IS NULL ON CONFLICT DO NOTHING`, userID, workspaceID)
	if err != nil {
		return domain.User{}, err
	}
	if len(groupIDs) > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO group_users (user_id, group_id)
SELECT $1, id FROM groups WHERE workspace_id = $2 AND id = ANY($3::uuid[]) AND deleted_at IS NULL ON CONFLICT DO NOTHING`, userID, workspaceID, groupIDs)
		if err != nil {
			return domain.User{}, err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM workspace_invitations WHERE id = $1`, invitationID); err != nil {
		return domain.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.User{}, err
	}
	return repository.UserByID(ctx, userID, workspaceID)
}

func (repository *Repository) UserByAuthAccount(ctx context.Context, providerID, providerUserID, workspaceID string) (domain.User, error) {
	return scanUser(repository.db.QueryRow(ctx, `
SELECT u.id::text, u.name, u.email, u.email_verified_at, u.password, u.avatar_url, u.role,
u.workspace_id::text, u.locale, u.timezone, COALESCE(u.settings, '{}'::jsonb),
u.last_active_at, u.last_login_at, u.deactivated_at, u.deleted_at, u.created_at,
u.updated_at, COALESCE(u.has_generated_password, false)
FROM users u
JOIN auth_accounts aa ON aa.user_id = u.id
WHERE aa.auth_provider_id = $1 AND aa.provider_user_id = $2
  AND aa.workspace_id = $3 AND aa.deleted_at IS NULL
  AND u.deleted_at IS NULL`, providerID, providerUserID, workspaceID))
}

func (repository *Repository) LinkAuthAccount(ctx context.Context, userID, providerID, providerUserID, workspaceID string) error {
	accountID, err := newUUID()
	if err != nil {
		return err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO auth_accounts (id, user_id, provider_user_id, auth_provider_id, workspace_id)
VALUES ($1, $2, $3, $4, $5)`, accountID, userID, providerUserID, providerID, workspaceID)
	return err
}

func (repository *Repository) CreateSSOUser(ctx context.Context, workspaceID, providerID, providerUserID, name, email, role string, allowSignup bool) (domain.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if email == "" || !strings.Contains(email, "@") || providerID == "" || providerUserID == "" {
		return domain.User{}, ErrInvalidInput
	}
	if role == "" || role == "owner" {
		role = "member"
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(email) = lower($1) AND workspace_id = $2 AND deleted_at IS NULL FOR UPDATE`, email, workspaceID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		if !allowSignup {
			return domain.User{}, ErrNotFound
		}
		userID, err = newUUID()
		if err != nil {
			return domain.User{}, err
		}
		if name == "" {
			name = email
		}
		_, err = tx.Exec(ctx, `INSERT INTO users
(id, name, email, email_verified_at, password, role, workspace_id, settings, has_generated_password)
VALUES ($1, $2, $3, now(), NULL, $4, $5, '{"preferences":{"fullPageWidth":false,"pageEditMode":"edit","editorToolbar":true}}'::jsonb, true)`, userID, name, email, role, workspaceID)
		if err != nil {
			return domain.User{}, err
		}
	} else if err != nil {
		return domain.User{}, err
	}
	accountID, err := newUUID()
	if err != nil {
		return domain.User{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth_accounts (id, user_id, provider_user_id, auth_provider_id, workspace_id)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, accountID, userID, providerUserID, providerID, workspaceID)
	if err != nil {
		return domain.User{}, err
	}
	groupMembershipID, err := newUUID()
	if err != nil {
		return domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO group_users (id, user_id, group_id)
SELECT $1, $2, id FROM groups WHERE workspace_id = $3 AND is_default = true AND deleted_at IS NULL ON CONFLICT DO NOTHING`, groupMembershipID, userID, workspaceID); err != nil {
		return domain.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.User{}, err
	}
	return repository.UserByID(ctx, userID, workspaceID)
}

func (repository *Repository) ChangePassword(ctx context.Context, userID, workspaceID, passwordHash, currentSessionID string) error {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE users SET password = $3, has_generated_password = false, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, userID, workspaceID, passwordHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if currentSessionID == "" {
		_, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, userID, workspaceID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND workspace_id = $2 AND id <> $3 AND revoked_at IS NULL`, userID, workspaceID, currentSessionID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (repository *Repository) CreatePasswordResetToken(ctx context.Context, userID, workspaceID string, expiresAt time.Time) (string, error) {
	token, err := randomID(24)
	if err != nil {
		return "", err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM user_tokens WHERE user_id = $1 AND workspace_id = $2 AND type = 'forgot-password'`, userID, workspaceID); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_tokens (token, type, user_id, workspace_id, expires_at) VALUES ($1, 'forgot-password', $2, $3, $4)`, token, userID, workspaceID, expiresAt); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

func (repository *Repository) PasswordResetTokenValid(ctx context.Context, token, tokenType, workspaceID string) (bool, error) {
	var valid bool
	err := repository.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_tokens WHERE token = $1 AND type = $2 AND workspace_id = $3 AND used_at IS NULL AND expires_at > now())`, token, tokenType, workspaceID).Scan(&valid)
	return valid, err
}

func (repository *Repository) ResetPassword(ctx context.Context, token, workspaceID, passwordHash string) (domain.User, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM user_tokens WHERE token = $1 AND workspace_id = $2 AND type = 'forgot-password' AND used_at IS NULL AND expires_at > now() FOR UPDATE`, token, workspaceID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password = $3, has_generated_password = false, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, userID, workspaceID, passwordHash); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_tokens WHERE user_id = $1 AND workspace_id = $2 AND type = 'forgot-password'`, userID, workspaceID); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, userID, workspaceID); err != nil {
		return domain.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.User{}, err
	}
	return repository.UserByID(ctx, userID, workspaceID)
}
