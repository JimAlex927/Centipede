package postgres

import (
	"context"
	"errors"
	"time"

	"centipede/internal/modules/identity/application"
	"centipede/internal/modules/identity/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (repository *Repository) UpsertExternalIdentity(ctx context.Context, identity domain.ExternalIdentity) (domain.User, error) {
	row := repository.pool.QueryRow(ctx, `
		INSERT INTO app_users (identity_issuer, identity_subject, email_snapshot)
		VALUES ($1, $2, NULLIF($3, ''))
		ON CONFLICT (identity_issuer, identity_subject) DO UPDATE
		SET email_snapshot = COALESCE(EXCLUDED.email_snapshot, app_users.email_snapshot),
			updated_at = now()
		RETURNING id, identity_issuer, identity_subject, COALESCE(email_snapshot, ''), COALESCE(display_name, ''), COALESCE(password_hash, ''), status, created_at, updated_at
	`, identity.Issuer, identity.Subject, identity.Email)
	return scanUser(row)
}

func (repository *Repository) FindByID(ctx context.Context, id int64) (domain.User, error) {
	row := repository.pool.QueryRow(ctx, `
		SELECT id, identity_issuer, identity_subject, COALESCE(email_snapshot, ''), COALESCE(display_name, ''), COALESCE(password_hash, ''), status, created_at, updated_at
		FROM app_users
		WHERE id = $1
	`, id)
	return scanUser(row)
}

func (repository *Repository) CreateLocalUser(ctx context.Context, user domain.User) (domain.User, error) {
	row := repository.pool.QueryRow(ctx, `
		INSERT INTO app_users (identity_issuer, identity_subject, email_snapshot, display_name, password_hash)
		VALUES ('local', $1, $2, $3, $4)
		RETURNING id, identity_issuer, identity_subject, COALESCE(email_snapshot, ''), COALESCE(display_name, ''), COALESCE(password_hash, ''), status, created_at, updated_at
	`, user.IdentitySubject, user.Email, user.DisplayName, user.PasswordHash)
	return scanUser(row)
}

func (repository *Repository) FindLocalByEmail(ctx context.Context, email string) (domain.User, error) {
	row := repository.pool.QueryRow(ctx, `
		SELECT id, identity_issuer, identity_subject, COALESCE(email_snapshot, ''), COALESCE(display_name, ''), COALESCE(password_hash, ''), status, created_at, updated_at
		FROM app_users
		WHERE identity_issuer = 'local' AND lower(email_snapshot) = lower($1)
	`, email)
	return scanUser(row)
}

func (repository *Repository) CreateSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO app_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, userID, tokenHash, expiresAt)
	return err
}

func (repository *Repository) FindActiveSession(ctx context.Context, tokenHash []byte, now time.Time) (application.Session, error) {
	row := repository.pool.QueryRow(ctx, `
		SELECT id, user_id, expires_at
		FROM app_sessions
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $2
	`, tokenHash, now)
	var session application.Session
	err := row.Scan(&session.ID, &session.UserID, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Session{}, pgx.ErrNoRows
	}
	return session, err
}

func (repository *Repository) RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE app_sessions SET revoked_at = COALESCE(revoked_at, $2), last_seen_at = $2
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash, now)
	return err
}

type rowScanner interface {
	Scan(...any) error
}

func scanUser(row rowScanner) (domain.User, error) {
	var user domain.User
	err := row.Scan(&user.ID, &user.IdentityIssuer, &user.IdentitySubject, &user.Email, &user.DisplayName, &user.PasswordHash, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, pgx.ErrNoRows
	}
	return user, err
}
