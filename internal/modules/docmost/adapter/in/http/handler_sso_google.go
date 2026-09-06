package http

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

const (
	googleOAuthIssuer = "https://accounts.google.com"
	googleUserScope   = "openid"
)

var googleOAuthEndpoint = oauth2.Endpoint{
	AuthURL:  googleOAuthIssuer + "/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// googleLogin starts the instance-level Google OAuth flow. Unlike custom SSO
// providers, Google credentials are configured once for the Go service and
// the workspace is selected by the public login page.
func (handler *Handler) googleLogin(c *gin.Context) {
	handler.startGoogleOAuth(c, false)
}

// googleSignup is used by the cloud signup button. Self-hosted deployments
// normally have a single workspace, so resolving that workspace keeps the
// route useful without introducing a second account-creation implementation.
// A multi-workspace installation can pass workspaceId just like the login
// route.
func (handler *Handler) googleSignup(c *gin.Context) {
	handler.startGoogleOAuth(c, true)
}

func (handler *Handler) startGoogleOAuth(c *gin.Context, signup bool) {
	workspace, err := handler.resolveSSOWorkspace(c, strings.TrimSpace(c.Query("workspaceId")))
	if err != nil {
		writeError(c, http.StatusNotFound, "Workspace not found")
		return
	}
	provider, err := handler.googleProvider(c, workspace.ID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Google SSO provider not found")
		return
	}
	if !provider.IsEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "sso:google") {
		writeError(c, http.StatusNotFound, "Google SSO provider not found")
		return
	}
	if signup && !provider.AllowSignup {
		writeError(c, http.StatusForbidden, "Google SSO signup is disabled")
		return
	}

	config, ok := handler.googleOAuthConfig(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Google SSO is not configured")
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
		WorkspaceID:  workspace.ID,
		Type:         "sso_state",
		ProviderID:   provider.ID,
		Redirect:     safeSSORedirect(c.Query("redirect")),
		Nonce:        nonce,
		CodeVerifier: verifier,
	}, ssoStateTTL)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SSO state")
		return
	}

	authURL := config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", s256(verifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	c.Redirect(http.StatusFound, authURL)
}

func (handler *Handler) googleCallback(c *gin.Context) {
	state, err := handler.tokens.parse(strings.TrimSpace(c.Query("state")), "sso_state")
	if err != nil || state.ProviderID == "" {
		writeError(c, http.StatusBadRequest, "Invalid SSO state")
		return
	}
	if providerError := strings.TrimSpace(c.Query("error")); providerError != "" {
		writeError(c, http.StatusUnauthorized, "Google authentication was cancelled")
		return
	}
	workspace, err := handler.repository.WorkspaceByID(c.Request.Context(), state.WorkspaceID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Workspace not found")
		return
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), state.ProviderID, workspace.ID)
	if err != nil || provider.Type != "google" || !provider.IsEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "sso:google") {
		writeError(c, http.StatusNotFound, "Google SSO provider not found")
		return
	}
	config, ok := handler.googleOAuthConfig(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Google SSO is not configured")
		return
	}
	token, err := config.Exchange(c.Request.Context(), strings.TrimSpace(c.Query("code")), oauth2.SetAuthURLParam("code_verifier", state.CodeVerifier))
	if err != nil {
		writeError(c, http.StatusUnauthorized, "Failed to exchange Google authorization code")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		writeError(c, http.StatusUnauthorized, "Google did not return an ID token")
		return
	}
	googleProvider, err := oidc.NewProvider(c.Request.Context(), googleOAuthIssuer)
	if err != nil {
		writeError(c, http.StatusBadGateway, "Failed to discover Google identity provider")
		return
	}
	idToken, err := googleProvider.Verifier(&oidc.Config{ClientID: config.ClientID}).Verify(c.Request.Context(), rawIDToken)
	if err != nil || idToken.Nonce != state.Nonce {
		writeError(c, http.StatusUnauthorized, "Invalid Google identity token")
		return
	}
	var claims struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		GivenName     string `json:"given_name"`
		FamilyName    string `json:"family_name"`
	}
	if err := idToken.Claims(&claims); err != nil || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.Email) == "" {
		writeError(c, http.StatusUnauthorized, "Google identity is missing required claims")
		return
	}
	if !claims.EmailVerified {
		writeError(c, http.StatusUnauthorized, "Google email is not verified")
		return
	}

	user, err := handler.repository.UserByAuthAccount(c.Request.Context(), provider.ID, claims.Subject, workspace.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		name := strings.TrimSpace(claims.Name)
		if name == "" {
			name = strings.TrimSpace(strings.TrimSpace(claims.GivenName) + " " + strings.TrimSpace(claims.FamilyName))
		}
		user, err = handler.repository.CreateSSOUser(c.Request.Context(), workspace.ID, provider.ID, claims.Subject, name, claims.Email, workspace.DefaultRole, provider.AllowSignup, handler.licenseSeatLimit(c.Request.Context(), workspace.ID))
	} else if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load Google SSO account")
		return
	}
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusForbidden, "Google SSO signup is disabled for this provider")
		} else if errors.Is(err, postgres.ErrLicenseSeatsExceeded) {
			writeError(c, http.StatusConflict, "The active license seat limit has been reached")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to create Google SSO account")
		}
		return
	}
	if err := handler.finishSSOLogin(c, user, workspace, state.Redirect); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
	}
}

func (handler *Handler) googleProvider(c *gin.Context, workspaceID string) (postgres.AuthProvider, error) {
	providers, err := handler.repository.AuthProviders(c.Request.Context(), workspaceID)
	if err != nil {
		return postgres.AuthProvider{}, err
	}
	for _, provider := range providers.Items {
		if strings.EqualFold(strings.TrimSpace(provider.Type), "google") {
			return provider, nil
		}
	}
	return postgres.AuthProvider{}, postgres.ErrNotFound
}

func (handler *Handler) googleOAuthConfig(c *gin.Context) (oauth2.Config, bool) {
	clientID := firstNonEmptyEnv("GOOGLE_CLIENT_ID", "GOOGLE_SSO_CLIENT_ID")
	clientSecret := firstNonEmptyEnv("GOOGLE_CLIENT_SECRET", "GOOGLE_SSO_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return oauth2.Config{}, false
	}
	return oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     googleOAuthEndpoint,
		RedirectURL:  handler.backendURL(c) + "/api/sso/google/callback",
		Scopes:       []string{googleUserScope, "email", "profile"},
	}, true
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
