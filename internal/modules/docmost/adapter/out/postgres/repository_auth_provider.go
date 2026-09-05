package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

type AuthProvider struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Type                 string          `json:"type"`
	SAMLURL              *string         `json:"samlUrl"`
	SAMLCertificate      *string         `json:"samlCertificate"`
	OIDCIssuer           *string         `json:"oidcIssuer"`
	OIDCClientID         *string         `json:"oidcClientId"`
	OIDCClientSecret     *string         `json:"oidcClientSecret"`
	LDAPURL              *string         `json:"ldapUrl"`
	LDAPBindDN           *string         `json:"ldapBindDn"`
	LDAPBindPassword     *string         `json:"ldapBindPassword"`
	LDAPBaseDN           *string         `json:"ldapBaseDn"`
	LDAPUserSearchFilter *string         `json:"ldapUserSearchFilter"`
	LDAPUserAttributes   json.RawMessage `json:"ldapUserAttributes"`
	LDAPTLSEnabled       bool            `json:"ldapTlsEnabled"`
	LDAPTLSCACert        *string         `json:"ldapTlsCaCert"`
	AllowSignup          bool            `json:"allowSignup"`
	IsEnabled            bool            `json:"isEnabled"`
	GroupSync            bool            `json:"groupSync"`
	CreatorID            *string         `json:"creatorId"`
	WorkspaceID          string          `json:"workspaceId"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
	DeletedAt            *time.Time      `json:"deletedAt"`
}

type PublicAuthProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"isEnabled"`
}

const authProviderColumns = `
p.id::text, p.name, p.type, p.saml_url, p.saml_certificate,
p.oidc_issuer, p.oidc_client_id, p.oidc_client_secret,
p.ldap_url, p.ldap_bind_dn, p.ldap_bind_password, p.ldap_base_dn,
p.ldap_user_search_filter, COALESCE(p.ldap_user_attributes, '{}'::jsonb),
COALESCE(p.ldap_tls_enabled, false), p.ldap_tls_ca_cert,
COALESCE(p.allow_signup, false), COALESCE(p.is_enabled, false),
COALESCE(p.group_sync, false), p.creator_id::text, p.workspace_id::text,
p.created_at, p.updated_at, p.deleted_at`

func scanAuthProvider(row rowScanner) (AuthProvider, error) {
	var value AuthProvider
	err := row.Scan(
		&value.ID, &value.Name, &value.Type, &value.SAMLURL, &value.SAMLCertificate,
		&value.OIDCIssuer, &value.OIDCClientID, &value.OIDCClientSecret,
		&value.LDAPURL, &value.LDAPBindDN, &value.LDAPBindPassword, &value.LDAPBaseDN,
		&value.LDAPUserSearchFilter, &value.LDAPUserAttributes, &value.LDAPTLSEnabled, &value.LDAPTLSCACert,
		&value.AllowSignup, &value.IsEnabled, &value.GroupSync, &value.CreatorID, &value.WorkspaceID,
		&value.CreatedAt, &value.UpdatedAt, &value.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthProvider{}, ErrNotFound
	}
	return value, err
}

func (repository *Repository) AuthProviders(ctx context.Context, workspaceID string) (domain.Pagination[AuthProvider], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+authProviderColumns+` FROM auth_providers p WHERE p.workspace_id = $1 AND p.deleted_at IS NULL ORDER BY p.is_enabled DESC, p.name, p.id`, workspaceID)
	if err != nil {
		return domain.Pagination[AuthProvider]{}, err
	}
	defer rows.Close()
	items := make([]AuthProvider, 0)
	for rows.Next() {
		item, scanErr := scanAuthProvider(rows)
		if scanErr != nil {
			return domain.Pagination[AuthProvider]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, len(items)), rows.Err()
}

func (repository *Repository) EnabledAuthProviders(ctx context.Context, workspaceID string) ([]PublicAuthProvider, error) {
	rows, err := repository.db.Query(ctx, `
SELECT p.id::text, p.name, p.type, p.is_enabled
FROM auth_providers p
WHERE p.workspace_id = $1 AND p.is_enabled = true AND p.deleted_at IS NULL
ORDER BY p.name, p.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PublicAuthProvider, 0)
	for rows.Next() {
		var item PublicAuthProvider
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.Enabled); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) AuthProviderByID(ctx context.Context, id, workspaceID string) (AuthProvider, error) {
	return scanAuthProvider(repository.db.QueryRow(ctx, `SELECT `+authProviderColumns+` FROM auth_providers p WHERE p.id = $1 AND p.workspace_id = $2 AND p.deleted_at IS NULL`, id, workspaceID))
}

func (repository *Repository) CreateAuthProvider(ctx context.Context, workspaceID, creatorID, name, providerType string) (AuthProvider, error) {
	id, err := newUUID()
	if err != nil {
		return AuthProvider{}, err
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO auth_providers (id, name, type, creator_id, workspace_id) VALUES ($1, $2, $3, $4, $5)`, id, name, providerType, creatorID, workspaceID)
	if err != nil {
		return AuthProvider{}, err
	}
	return repository.AuthProviderByID(ctx, id, workspaceID)
}

type AuthProviderUpdate struct {
	Name             *string
	SAMLURL          *string
	SAMLCertificate  *string
	OIDCIssuer       *string
	OIDCClientID     *string
	OIDCClientSecret *string
	LDAPURL          *string
	LDAPBindDN       *string
	LDAPBindPassword *string
	LDAPBaseDN       *string
	LDAPSearchFilter *string
	LDAPTLSEnabled   *bool
	LDAPTLSCACert    *string
	AllowSignup      *bool
	IsEnabled        *bool
	GroupSync        *bool
}

func (repository *Repository) UpdateAuthProvider(ctx context.Context, id, workspaceID string, input AuthProviderUpdate) (AuthProvider, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return AuthProvider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if input.GroupSync != nil && *input.GroupSync {
		if _, err = tx.Exec(ctx, `
UPDATE auth_providers SET group_sync = false, updated_at = now()
WHERE workspace_id = $1 AND id <> $2 AND deleted_at IS NULL`, workspaceID, id); err != nil {
			return AuthProvider{}, err
		}
	}
	result, err := tx.Exec(ctx, `
UPDATE auth_providers SET
  name = COALESCE($3, name), saml_url = COALESCE($4, saml_url),
  saml_certificate = COALESCE($5, saml_certificate), oidc_issuer = COALESCE($6, oidc_issuer),
  oidc_client_id = COALESCE($7, oidc_client_id), oidc_client_secret = COALESCE($8, oidc_client_secret),
  ldap_url = COALESCE($9, ldap_url), ldap_bind_dn = COALESCE($10, ldap_bind_dn),
  ldap_bind_password = COALESCE($11, ldap_bind_password), ldap_base_dn = COALESCE($12, ldap_base_dn),
  ldap_user_search_filter = COALESCE($13, ldap_user_search_filter), ldap_tls_enabled = COALESCE($14, ldap_tls_enabled),
  ldap_tls_ca_cert = COALESCE($15, ldap_tls_ca_cert), allow_signup = COALESCE($16, allow_signup),
  is_enabled = COALESCE($17, is_enabled), group_sync = COALESCE($18, group_sync), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID,
		input.Name, input.SAMLURL, input.SAMLCertificate, input.OIDCIssuer, input.OIDCClientID, input.OIDCClientSecret,
		input.LDAPURL, input.LDAPBindDN, input.LDAPBindPassword, input.LDAPBaseDN, input.LDAPSearchFilter,
		input.LDAPTLSEnabled, input.LDAPTLSCACert, input.AllowSignup, input.IsEnabled, input.GroupSync)
	if err != nil {
		return AuthProvider{}, err
	}
	if result.RowsAffected() == 0 {
		return AuthProvider{}, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return AuthProvider{}, err
	}
	return repository.AuthProviderByID(ctx, id, workspaceID)
}

func (repository *Repository) DeleteAuthProvider(ctx context.Context, id, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `UPDATE auth_providers SET deleted_at = now(), is_enabled = false, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
