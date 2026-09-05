package http

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

const ssoStateTTL = 10 * time.Minute

func (handler *Handler) oidcLogin(c *gin.Context) {
	providerID := strings.TrimSpace(c.Param("providerID"))
	workspaceID := strings.TrimSpace(c.Query("workspaceId"))
	workspace, err := handler.resolveSSOWorkspace(c, workspaceID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Workspace not found")
		return
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), providerID, workspace.ID)
	if err != nil || provider.Type != "oidc" || !provider.IsEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "sso:custom") {
		writeError(c, http.StatusNotFound, "SSO provider not found")
		return
	}
	issuer, clientID, clientSecret := strings.TrimSpace(valueOrDefault(provider.OIDCIssuer, "")), strings.TrimSpace(valueOrDefault(provider.OIDCClientID, "")), strings.TrimSpace(valueOrDefault(provider.OIDCClientSecret, ""))
	if issuer == "" || clientID == "" || clientSecret == "" {
		writeError(c, http.StatusBadRequest, "OIDC provider is not configured")
		return
	}
	oidcProvider, err := oidc.NewProvider(c.Request.Context(), issuer)
	if err != nil {
		writeError(c, http.StatusBadGateway, "Failed to discover OIDC provider")
		return
	}
	nonce, err := randomSSOValue(32)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SSO state")
		return
	}
	verifier, err := randomSSOValue(48)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SSO state")
		return
	}
	state, err := handler.tokens.issue(tokenClaims{
		WorkspaceID: workspace.ID, Type: "sso_state", ProviderID: provider.ID,
		Redirect: safeSSORedirect(c.Query("redirect")), Nonce: nonce, CodeVerifier: verifier,
	}, ssoStateTTL)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SSO state")
		return
	}
	callbackURL := handler.backendURL(c) + "/api/sso/oidc/" + url.PathEscape(provider.ID) + "/callback"
	config := oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: oidcProvider.Endpoint(), RedirectURL: callbackURL, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	authURL := config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", s256(verifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	c.Redirect(http.StatusFound, authURL)
}

func (handler *Handler) oidcCallback(c *gin.Context) {
	state, err := handler.tokens.parse(strings.TrimSpace(c.Query("state")), "sso_state")
	if err != nil || state.ProviderID == "" || state.ProviderID != strings.TrimSpace(c.Param("providerID")) {
		writeError(c, http.StatusBadRequest, "Invalid SSO state")
		return
	}
	if providerError := strings.TrimSpace(c.Query("error")); providerError != "" {
		writeError(c, http.StatusUnauthorized, "SSO authentication was cancelled")
		return
	}
	workspace, err := handler.repository.WorkspaceByID(c.Request.Context(), state.WorkspaceID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Workspace not found")
		return
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), state.ProviderID, workspace.ID)
	if err != nil || provider.Type != "oidc" || !provider.IsEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "sso:custom") {
		writeError(c, http.StatusNotFound, "SSO provider not found")
		return
	}
	issuer, clientID, clientSecret := strings.TrimSpace(valueOrDefault(provider.OIDCIssuer, "")), strings.TrimSpace(valueOrDefault(provider.OIDCClientID, "")), strings.TrimSpace(valueOrDefault(provider.OIDCClientSecret, ""))
	oidcProvider, err := oidc.NewProvider(c.Request.Context(), issuer)
	if err != nil {
		writeError(c, http.StatusBadGateway, "Failed to discover OIDC provider")
		return
	}
	callbackURL := handler.backendURL(c) + "/api/sso/oidc/" + url.PathEscape(provider.ID) + "/callback"
	config := oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: oidcProvider.Endpoint(), RedirectURL: callbackURL, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	token, err := config.Exchange(c.Request.Context(), c.Query("code"), oauth2.SetAuthURLParam("code_verifier", state.CodeVerifier))
	if err != nil {
		writeError(c, http.StatusUnauthorized, "Failed to exchange OIDC authorization code")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		writeError(c, http.StatusUnauthorized, "OIDC provider did not return an ID token")
		return
	}
	idToken, err := oidcProvider.Verifier(&oidc.Config{ClientID: clientID}).Verify(c.Request.Context(), rawIDToken)
	if err != nil || idToken.Nonce != state.Nonce {
		writeError(c, http.StatusUnauthorized, "Invalid OIDC identity token")
		return
	}
	var claims struct {
		Subject       string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified bool            `json:"email_verified"`
		Name          string          `json:"name"`
		PreferredName string          `json:"preferred_username"`
		Groups        json.RawMessage `json:"groups"`
		Roles         json.RawMessage `json:"roles"`
	}
	if err := idToken.Claims(&claims); err != nil || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.Email) == "" {
		writeError(c, http.StatusUnauthorized, "OIDC identity is missing required claims")
		return
	}
	user, err := handler.repository.UserByAuthAccount(c.Request.Context(), provider.ID, claims.Subject, workspace.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		if !claims.EmailVerified {
			writeError(c, http.StatusUnauthorized, "OIDC email is not verified")
			return
		}
		name := strings.TrimSpace(claims.Name)
		if name == "" {
			name = strings.TrimSpace(claims.PreferredName)
		}
		user, err = handler.repository.CreateSSOUser(c.Request.Context(), workspace.ID, provider.ID, claims.Subject, name, claims.Email, workspace.DefaultRole, provider.AllowSignup)
	} else if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load SSO account")
		return
	}
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusForbidden, "SSO signup is disabled for this provider")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to create SSO account")
		}
		return
	}
	if provider.GroupSync {
		groups := append(ssoClaimValues(claims.Groups), ssoClaimValues(claims.Roles)...)
		if err := handler.repository.SyncSSOGroups(c.Request.Context(), user.ID, workspace.ID, groups); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to synchronize SSO groups")
			return
		}
	}
	if err := handler.finishSSOLogin(c, user, workspace, state.Redirect); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
		return
	}
}

func (handler *Handler) resolveSSOWorkspace(c *gin.Context, workspaceID string) (domain.Workspace, error) {
	if workspaceID != "" {
		return handler.repository.WorkspaceByID(c.Request.Context(), workspaceID)
	}
	return handler.repository.OnlyWorkspace(c.Request.Context())
}

func (handler *Handler) finishSSOLogin(c *gin.Context, user domain.User, workspace domain.Workspace, redirect string) error {
	if err := handler.repository.MarkLogin(c.Request.Context(), user.ID, workspace.ID); err != nil {
		return err
	}
	mfaRecord, mfaErr := handler.repository.MFAByUser(c.Request.Context(), user.ID, workspace.ID)
	if mfaErr != nil && !errors.Is(mfaErr, postgres.ErrNotFound) {
		return mfaErr
	}
	if (mfaErr == nil && mfaRecord.IsEnabled) || (workspace.EnforceMFA && (mfaErr != nil || !mfaRecord.IsEnabled)) {
		if _, err := handler.issueMFAChallenge(c, user); err != nil {
			return err
		}
		path := "/login/mfa"
		if workspace.EnforceMFA && (mfaErr != nil || !mfaRecord.IsEnabled) {
			path = "/login/mfa/setup"
		}
		c.Redirect(http.StatusFound, handler.frontendURL+path+redirectQuery(redirect))
		return nil
	}
	if err := handler.startSession(c, user); err != nil {
		return err
	}
	c.Redirect(http.StatusFound, handler.frontendURL+safeSSORedirect(redirect))
	return nil
}

func randomSSOValue(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func s256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func safeSSORedirect(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") || strings.Contains(value, "://") || strings.ContainsAny(value, "\r\n") {
		return "/home"
	}
	return value
}

func redirectQuery(value string) string {
	value = safeSSORedirect(value)
	if value == "/home" {
		return ""
	}
	return "?redirect=" + url.QueryEscape(value)
}
