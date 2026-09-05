package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// OAuthClient mirrors the OAuth tables used by the Docmost enterprise client.
// JSON fields are decoded here so handlers never need to know the database
// representation.
type OAuthClient struct {
	ID                      string     `json:"id"`
	Name                    string     `json:"name"`
	RedirectURIs            []string   `json:"redirectUris"`
	ClientURI               *string    `json:"clientUri"`
	LogoURI                 *string    `json:"logoUri"`
	GrantTypes              []string   `json:"grantTypes"`
	Scopes                  []string   `json:"scopes"`
	TokenEndpointAuthMethod string     `json:"tokenEndpointAuthMethod"`
	SecretHash              *string    `json:"-"`
	IsDynamic               bool       `json:"isDynamic"`
	WorkspaceID             string     `json:"workspaceId"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
	DeletedAt               *time.Time `json:"deletedAt"`
}

type OAuthGrant struct {
	ID           string     `json:"id"`
	ClientID     string     `json:"clientId"`
	ClientName   string     `json:"clientName"`
	RedirectURIs []string   `json:"redirectUris"`
	UserID       string     `json:"userId"`
	WorkspaceID  string     `json:"workspaceId"`
	Scopes       []string   `json:"scopes"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt"`
	RevokedAt    *time.Time `json:"-"`
}

type OAuthAuthorizationCode struct {
	ID                  string
	ClientID            string
	UserID              string
	WorkspaceID         string
	Scopes              []string
	RedirectURI         string
	CodeChallenge       *string
	CodeChallengeMethod *string
	ExpiresAt           time.Time
}

type OAuthToken struct {
	ID               string
	GrantID          string
	WorkspaceID      string
	Scopes           []string
	AccessTokenJTI   string
	RefreshTokenHash *string
	AccessExpiresAt  time.Time
	RefreshExpiresAt *time.Time
	RevokedAt        *time.Time
}

func scanOAuthJSON(raw []byte, destination *[]string) error {
	if len(raw) == 0 {
		*destination = []string{}
		return nil
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return err
	}
	if *destination == nil {
		*destination = []string{}
	}
	return nil
}

func scanOAuthClient(row rowScanner) (OAuthClient, error) {
	var value OAuthClient
	var redirects, grantTypes, scopes []byte
	err := row.Scan(&value.ID, &value.Name, &redirects, &value.ClientURI, &value.LogoURI,
		&grantTypes, &scopes, &value.TokenEndpointAuthMethod, &value.SecretHash,
		&value.IsDynamic, &value.WorkspaceID, &value.CreatedAt, &value.UpdatedAt, &value.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, err
	}
	if err := scanOAuthJSON(redirects, &value.RedirectURIs); err != nil {
		return OAuthClient{}, err
	}
	if err := scanOAuthJSON(grantTypes, &value.GrantTypes); err != nil {
		return OAuthClient{}, err
	}
	if err := scanOAuthJSON(scopes, &value.Scopes); err != nil {
		return OAuthClient{}, err
	}
	return value, nil
}

const oauthClientColumns = `
c.id::text, c.name, c.redirect_uris, c.client_uri, c.logo_uri,
c.grant_types, c.scopes, c.token_endpoint_auth_method, c.secret_hash,
c.is_dynamic, c.workspace_id::text, c.created_at, c.updated_at, c.deleted_at`

func (repository *Repository) OAuthClientByID(ctx context.Context, clientID, workspaceID string) (OAuthClient, error) {
	return scanOAuthClient(repository.db.QueryRow(ctx, `SELECT `+oauthClientColumns+` FROM oauth_clients c
WHERE c.id = $1 AND ($2 = '' OR c.workspace_id = $2) AND c.deleted_at IS NULL`, clientID, workspaceID))
}

func (repository *Repository) CreateOAuthClient(ctx context.Context, name string, redirectURIs, grantTypes, scopes []string, authMethod, secretHash, workspaceID string, dynamic bool) (OAuthClient, error) {
	id, err := newUUID()
	if err != nil {
		return OAuthClient{}, err
	}
	if authMethod == "" {
		authMethod = "none"
	}
	if grantTypes == nil {
		grantTypes = []string{"authorization_code"}
	}
	if scopes == nil {
		scopes = []string{"read", "write"}
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO oauth_clients
(id, name, redirect_uris, grant_types, scopes, token_endpoint_auth_method, secret_hash, is_dynamic, workspace_id)
VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb, $6, NULLIF($7, ''), $8, $9)`,
		id, name, marshalOAuthJSON(redirectURIs), marshalOAuthJSON(grantTypes), marshalOAuthJSON(scopes), authMethod, secretHash, dynamic, workspaceID)
	if err != nil {
		return OAuthClient{}, err
	}
	return repository.OAuthClientByID(ctx, id, workspaceID)
}

func marshalOAuthJSON(value []string) []byte {
	data, _ := json.Marshal(value)
	return data
}

func hashOAuthValue(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (repository *Repository) CreateOAuthAuthorizationCode(ctx context.Context, rawCode string, code OAuthAuthorizationCode) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO oauth_authorization_codes
(id, code_hash, client_id, user_id, workspace_id, scopes, redirect_uri, code_challenge, code_challenge_method, expires_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10)`, id, hashOAuthValue(rawCode), code.ClientID,
		code.UserID, code.WorkspaceID, marshalOAuthJSON(code.Scopes), code.RedirectURI,
		code.CodeChallenge, code.CodeChallengeMethod, code.ExpiresAt)
	return err
}

// ConsumeOAuthAuthorizationCode atomically consumes a one-time code. The
// redirect URI is checked again here to prevent code substitution.
func (repository *Repository) ConsumeOAuthAuthorizationCode(ctx context.Context, rawCode, clientID, redirectURI string) (OAuthAuthorizationCode, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return OAuthAuthorizationCode{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var code OAuthAuthorizationCode
	var scopes []byte
	err = tx.QueryRow(ctx, `SELECT id::text, client_id::text, user_id::text, workspace_id::text,
scopes, redirect_uri, code_challenge, code_challenge_method, expires_at
FROM oauth_authorization_codes
WHERE code_hash = $1 AND client_id = $2 AND redirect_uri = $3
  AND consumed_at IS NULL AND expires_at > now()
FOR UPDATE`, hashOAuthValue(rawCode), clientID, redirectURI).Scan(&code.ID, &code.ClientID, &code.UserID,
		&code.WorkspaceID, &scopes, &code.RedirectURI, &code.CodeChallenge, &code.CodeChallengeMethod, &code.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthAuthorizationCode{}, ErrNotFound
	}
	if err != nil {
		return OAuthAuthorizationCode{}, err
	}
	if err := scanOAuthJSON(scopes, &code.Scopes); err != nil {
		return OAuthAuthorizationCode{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE oauth_authorization_codes SET consumed_at = now() WHERE id = $1`, code.ID); err != nil {
		return OAuthAuthorizationCode{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OAuthAuthorizationCode{}, err
	}
	return code, nil
}

func (repository *Repository) UpsertOAuthGrant(ctx context.Context, userID, clientID, workspaceID string, scopes []string) (OAuthGrant, error) {
	id, err := newUUID()
	if err != nil {
		return OAuthGrant{}, err
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO oauth_grants (id, user_id, client_id, workspace_id, scopes)
VALUES ($1, $2, $3, $4, $5::jsonb)
ON CONFLICT (user_id, client_id) DO UPDATE SET scopes = EXCLUDED.scopes, revoked_at = NULL, updated_at = now()`,
		id, userID, clientID, workspaceID, marshalOAuthJSON(scopes))
	if err != nil {
		return OAuthGrant{}, err
	}
	return repository.OAuthGrantByUserClient(ctx, userID, clientID, workspaceID)
}

func (repository *Repository) OAuthGrantByUserClient(ctx context.Context, userID, clientID, workspaceID string) (OAuthGrant, error) {
	return repository.scanOAuthGrant(repository.db.QueryRow(ctx, `SELECT g.id::text, g.client_id::text, c.name, c.redirect_uris,
g.user_id::text, g.workspace_id::text, g.scopes, g.created_at, g.updated_at, g.last_used_at, g.revoked_at
FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
WHERE g.user_id = $1 AND g.client_id = $2 AND g.workspace_id = $3 AND g.revoked_at IS NULL
  AND c.deleted_at IS NULL`, userID, clientID, workspaceID))
}

func (repository *Repository) OAuthGrantByID(ctx context.Context, grantID, workspaceID string) (OAuthGrant, error) {
	return repository.scanOAuthGrant(repository.db.QueryRow(ctx, `SELECT g.id::text, g.client_id::text, c.name, c.redirect_uris,
g.user_id::text, g.workspace_id::text, g.scopes, g.created_at, g.updated_at, g.last_used_at, g.revoked_at
FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
WHERE g.id = $1 AND g.workspace_id = $2 AND g.revoked_at IS NULL AND c.deleted_at IS NULL`, grantID, workspaceID))
}

func (repository *Repository) OAuthGrants(ctx context.Context, userID, workspaceID string) ([]OAuthGrant, error) {
	rows, err := repository.db.Query(ctx, `SELECT g.id::text, g.client_id::text, c.name, c.redirect_uris,
g.user_id::text, g.workspace_id::text, g.scopes, g.created_at, g.updated_at, g.last_used_at, g.revoked_at
FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
WHERE g.user_id = $1 AND g.workspace_id = $2 AND g.revoked_at IS NULL AND c.deleted_at IS NULL
ORDER BY g.created_at DESC, g.id`, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]OAuthGrant, 0)
	for rows.Next() {
		item, scanErr := repository.scanOAuthGrant(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) scanOAuthGrant(row rowScanner) (OAuthGrant, error) {
	var value OAuthGrant
	var redirectURIs, scopes []byte
	err := row.Scan(&value.ID, &value.ClientID, &value.ClientName, &redirectURIs, &value.UserID,
		&value.WorkspaceID, &scopes, &value.CreatedAt, &value.UpdatedAt, &value.LastUsedAt, &value.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, err
	}
	if err := scanOAuthJSON(redirectURIs, &value.RedirectURIs); err != nil {
		return OAuthGrant{}, err
	}
	if err := scanOAuthJSON(scopes, &value.Scopes); err != nil {
		return OAuthGrant{}, err
	}
	return value, nil
}

func (repository *Repository) RevokeOAuthGrant(ctx context.Context, grantID, userID, workspaceID string) error {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE oauth_grants SET revoked_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND workspace_id = $3 AND revoked_at IS NULL`, grantID, userID, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE oauth_tokens SET revoked_at = now() WHERE grant_id = $1 AND revoked_at IS NULL`, grantID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (repository *Repository) CreateOAuthToken(ctx context.Context, token OAuthToken) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	refreshHash := ""
	if value := oauthValueOrEmpty(token.RefreshTokenHash); value != "" {
		refreshHash = hashOAuthValue(value)
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO oauth_tokens
(id, grant_id, workspace_id, access_token_jti, refresh_token_hash, scopes, access_expires_at, refresh_expires_at)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb, $7, $8)`, id, token.GrantID, token.WorkspaceID,
		token.AccessTokenJTI, refreshHash, marshalOAuthJSON(token.Scopes), token.AccessExpiresAt, token.RefreshExpiresAt)
	return err
}

func oauthValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (repository *Repository) OAuthTokenByRefreshHash(ctx context.Context, refreshToken string) (OAuthToken, error) {
	var token OAuthToken
	var scopes []byte
	err := repository.db.QueryRow(ctx, `SELECT id::text, grant_id::text, workspace_id::text, access_token_jti,
refresh_token_hash, scopes, access_expires_at, refresh_expires_at, revoked_at
FROM oauth_tokens WHERE refresh_token_hash = $1 AND revoked_at IS NULL
  AND refresh_expires_at IS NOT NULL AND refresh_expires_at > now()`, hashOAuthValue(refreshToken)).Scan(
		&token.ID, &token.GrantID, &token.WorkspaceID, &token.AccessTokenJTI, &token.RefreshTokenHash,
		&scopes, &token.AccessExpiresAt, &token.RefreshExpiresAt, &token.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthToken{}, ErrNotFound
	}
	if err != nil {
		return OAuthToken{}, err
	}
	if err := scanOAuthJSON(scopes, &token.Scopes); err != nil {
		return OAuthToken{}, err
	}
	return token, nil
}

func (repository *Repository) OAuthAccessTokenActive(ctx context.Context, jti, userID, workspaceID, grantID string) (bool, error) {
	var active bool
	err := repository.db.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM oauth_tokens t JOIN oauth_grants g ON g.id = t.grant_id
WHERE t.access_token_jti = $1 AND t.workspace_id = $2 AND g.user_id = $3 AND g.id = $4
  AND t.revoked_at IS NULL AND g.revoked_at IS NULL AND t.access_expires_at > now())`,
		jti, workspaceID, userID, grantID).Scan(&active)
	return active, err
}

func (repository *Repository) TouchOAuthGrant(ctx context.Context, grantID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE oauth_grants SET last_used_at = now(), updated_at = now()
WHERE id = $1 AND revoked_at IS NULL`, grantID)
	return err
}

func (repository *Repository) RevokeOAuthToken(ctx context.Context, tokenID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE oauth_tokens SET revoked_at = now()
WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, tokenID, workspaceID)
	return err
}
