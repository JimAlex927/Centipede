package http

import (
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

type ssoProviderRequest struct {
	ProviderID       string  `json:"providerId"`
	Name             *string `json:"name"`
	Type             string  `json:"type"`
	SAMLURL          *string `json:"samlUrl"`
	SAMLCertificate  *string `json:"samlCertificate"`
	OIDCIssuer       *string `json:"oidcIssuer"`
	OIDCClientID     *string `json:"oidcClientId"`
	OIDCClientSecret *string `json:"oidcClientSecret"`
	LDAPURL          *string `json:"ldapUrl"`
	LDAPBindDN       *string `json:"ldapBindDn"`
	LDAPBindPassword *string `json:"ldapBindPassword"`
	LDAPBaseDN       *string `json:"ldapBaseDn"`
	LDAPSearchFilter *string `json:"ldapUserSearchFilter"`
	LDAPTLSEnabled   *bool   `json:"ldapTlsEnabled"`
	LDAPTLSCACert    *string `json:"ldapTlsCaCert"`
	AllowSignup      *bool   `json:"allowSignup"`
	IsEnabled        *bool   `json:"isEnabled"`
	GroupSync        *bool   `json:"groupSync"`
}

func (handler *Handler) ssoProviders(c *gin.Context) {
	if !handler.hasSSOFeature(c) {
		writeError(c, http.StatusForbidden, "This feature requires an active license")
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	result, err := handler.repository.AuthProviders(c.Request.Context(), current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load SSO providers")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) ssoInfo(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request ssoProviderRequest
	if !decodeOptional(c, &request) {
		return
	}
	providerID := strings.TrimSpace(request.ProviderID)
	var provider postgres.AuthProvider
	var err error
	if providerID != "" {
		provider, err = handler.repository.AuthProviderByID(c.Request.Context(), providerID, current.Workspace.ID)
	} else {
		providers, listErr := handler.repository.AuthProviders(c.Request.Context(), current.Workspace.ID)
		if listErr == nil && len(providers.Items) > 0 {
			provider = providers.Items[0]
		} else {
			err = postgres.ErrNotFound
		}
	}
	if err != nil {
		handler.writeRepositoryError(c, err, "SSO provider not found")
		return
	}
	if !handler.hasProviderFeature(c, provider.Type) {
		writeError(c, http.StatusForbidden, "This feature requires an active license")
		return
	}
	writeData(c, http.StatusOK, provider)
}

func (handler *Handler) createSSOProvider(c *gin.Context) {
	var request ssoProviderRequest
	if !decode(c, &request) {
		return
	}
	providerType := strings.ToLower(strings.TrimSpace(request.Type))
	if providerType != "oidc" && providerType != "saml" && providerType != "ldap" {
		writeError(c, http.StatusBadRequest, "Unsupported SSO provider type")
		return
	}
	if !handler.requireFeature(c, "sso:custom") {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	name := strings.TrimSpace(valueOrDefault(request.Name, strings.ToUpper(providerType)))
	if name == "" || len(name) > 120 {
		writeError(c, http.StatusBadRequest, "SSO provider name is required")
		return
	}
	provider, err := handler.repository.CreateAuthProvider(c.Request.Context(), current.Workspace.ID, current.User.ID, name, providerType)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SSO provider")
		return
	}
	actorID, resourceID := current.User.ID, provider.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "sso.provider_created", ResourceType: "auth_provider", ResourceID: &resourceID})
	writeData(c, http.StatusOK, provider)
}

func (handler *Handler) updateSSOProvider(c *gin.Context) {
	var request ssoProviderRequest
	if !decode(c, &request) || strings.TrimSpace(request.ProviderID) == "" {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), request.ProviderID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "SSO provider not found")
		return
	}
	if !handler.hasProviderFeature(c, provider.Type) {
		writeError(c, http.StatusForbidden, "This feature requires an active license")
		return
	}
	updated, err := handler.repository.UpdateAuthProvider(c.Request.Context(), provider.ID, current.Workspace.ID, postgres.AuthProviderUpdate{
		Name: request.Name, SAMLURL: request.SAMLURL, SAMLCertificate: request.SAMLCertificate,
		OIDCIssuer: request.OIDCIssuer, OIDCClientID: request.OIDCClientID, OIDCClientSecret: request.OIDCClientSecret,
		LDAPURL: request.LDAPURL, LDAPBindDN: request.LDAPBindDN, LDAPBindPassword: request.LDAPBindPassword,
		LDAPBaseDN: request.LDAPBaseDN, LDAPSearchFilter: request.LDAPSearchFilter, LDAPTLSEnabled: request.LDAPTLSEnabled,
		LDAPTLSCACert: request.LDAPTLSCACert, AllowSignup: request.AllowSignup, IsEnabled: request.IsEnabled, GroupSync: request.GroupSync,
	})
	if err != nil {
		handler.writeRepositoryError(c, err, "SSO provider not found")
		return
	}
	actorID, resourceID := current.User.ID, updated.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "sso.provider_updated", ResourceType: "auth_provider", ResourceID: &resourceID})
	writeData(c, http.StatusOK, updated)
}

func (handler *Handler) deleteSSOProvider(c *gin.Context) {
	var request ssoProviderRequest
	if !decode(c, &request) || strings.TrimSpace(request.ProviderID) == "" {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), request.ProviderID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "SSO provider not found")
		return
	}
	if !handler.hasProviderFeature(c, provider.Type) {
		writeError(c, http.StatusForbidden, "This feature requires an active license")
		return
	}
	if err = handler.repository.DeleteAuthProvider(c.Request.Context(), provider.ID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "SSO provider not found")
		return
	}
	actorID, resourceID := current.User.ID, provider.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "sso.provider_deleted", ResourceType: "auth_provider", ResourceID: &resourceID})
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) hasSSOFeature(c *gin.Context) bool {
	return handler.hasProviderFeature(c, "oidc") || handler.hasProviderFeature(c, "google")
}

func (handler *Handler) hasProviderFeature(c *gin.Context, providerType string) bool {
	feature := "sso:custom"
	if providerType == "google" {
		feature = "sso:google"
	}
	features, _ := handler.entitlementInfo(c)["features"].([]string)
	for _, candidate := range features {
		if candidate == feature {
			return true
		}
	}
	return false
}

func valueOrDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
