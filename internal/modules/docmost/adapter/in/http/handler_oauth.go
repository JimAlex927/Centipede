package http

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const (
	oauthAccessTTL  = time.Hour
	oauthRefreshTTL = 30 * 24 * time.Hour
	oauthCodeTTL    = 5 * time.Minute
)

type oauthAuthorizeParams struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Scope               string `json:"scope"`
	Resource            string `json:"resource"`
}

type oauthApprovalRequest struct {
	oauthAuthorizeParams
	Approved       bool     `json:"approved"`
	ApprovedScopes []string `json:"approvedScopes"`
}

type oauthRegisterRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientURI               string   `json:"client_uri"`
	LogoURI                 string   `json:"logo_uri"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (handler *Handler) oauthMetadata(c *gin.Context) {
	base := handler.backendURL(c)
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                base,
		"authorization_endpoint":                base + "/api/oauth/authorize",
		"token_endpoint":                        base + "/api/oauth/token",
		"registration_endpoint":                 base + "/api/oauth/register",
		"scopes_supported":                      []string{"read", "write"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"},
	})
}

func (handler *Handler) oauthProtectedResourceMetadata(c *gin.Context) {
	base := handler.backendURL(c)
	c.JSON(http.StatusOK, gin.H{
		"resource":              base + "/api/mcp",
		"authorization_servers": []string{base},
		"scopes_supported":      []string{"read", "write"},
	})
}

func (handler *Handler) oauthAuthorizeEndpoint(c *gin.Context) {
	params := oauthAuthorizeParamsFromValues(c.Request.URL.Query())
	workspace, client, scopes, err := handler.validateOAuthAuthorize(c, params, strings.TrimSpace(c.Query("workspaceId")))
	if err != nil {
		handler.writeOAuthAuthorizeError(c, params, err)
		return
	}
	if !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "oauth") {
		oauthError(c, http.StatusForbidden, "access_denied", "OAuth is not enabled for this workspace")
		return
	}
	_ = client
	_ = scopes
	values := c.Request.URL.Query()
	values.Set("client_id", params.ClientID)
	values.Set("redirect_uri", params.RedirectURI)
	values.Set("response_type", params.ResponseType)
	if params.State != "" {
		values.Set("state", params.State)
	}
	frontend := handler.publicFrontendURL(c) + "/oauth/consent?" + values.Encode()
	c.Redirect(http.StatusFound, frontend)
}

func (handler *Handler) oauthAuthorizeInfo(c *gin.Context) {
	if !handler.requireFeature(c, "oauth") {
		return
	}
	var params oauthAuthorizeParams
	if !decode(c, &params) {
		return
	}
	current := currentPrincipal(c)
	workspace, client, scopes, err := handler.validateOAuthAuthorize(c, params, current.Workspace.ID)
	if err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeData(c, http.StatusOK, gin.H{
		"clientName": client.Name, "redirectUri": params.RedirectURI, "scopes": scopes,
		"clientCreatedAt": client.CreatedAt, "verified": !client.IsDynamic,
		"workspaceId": workspace.ID,
	})
}

func (handler *Handler) oauthApprove(c *gin.Context) {
	if !handler.requireFeature(c, "oauth") {
		return
	}
	var request oauthApprovalRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	_, client, requestedScopes, err := handler.validateOAuthAuthorize(c, request.oauthAuthorizeParams, current.Workspace.ID)
	if err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !request.Approved {
		writeData(c, http.StatusOK, gin.H{"redirectUrl": oauthRedirectError(request.RedirectURI, request.State, "access_denied")})
		return
	}
	approved := normalizeOAuthScopes(request.ApprovedScopes)
	if len(approved) == 0 {
		oauthError(c, http.StatusBadRequest, "invalid_scope", "At least one scope must be approved")
		return
	}
	for _, scope := range approved {
		if !containsString(requestedScopes, scope) {
			oauthError(c, http.StatusBadRequest, "invalid_scope", "Approved scope was not requested")
			return
		}
	}
	grant, err := handler.repository.UpsertOAuthGrant(c.Request.Context(), current.User.ID, client.ID, current.Workspace.ID, approved)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create OAuth grant")
		return
	}
	rawCode, err := randomSSOValue(32)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create authorization code")
		return
	}
	if err := handler.repository.CreateOAuthAuthorizationCode(c.Request.Context(), rawCode, postgres.OAuthAuthorizationCode{
		ClientID: client.ID, UserID: current.User.ID, WorkspaceID: current.Workspace.ID, Scopes: approved,
		RedirectURI: request.RedirectURI, CodeChallenge: nonEmptyPointer(request.CodeChallenge),
		CodeChallengeMethod: nonEmptyPointer(request.CodeChallengeMethod), ExpiresAt: time.Now().UTC().Add(oauthCodeTTL),
	}); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create authorization code")
		return
	}
	_ = grant
	writeData(c, http.StatusOK, gin.H{"redirectUrl": oauthRedirectCode(request.RedirectURI, request.State, rawCode)})
}

func (handler *Handler) oauthGrants(c *gin.Context) {
	if !handler.requireFeature(c, "oauth") {
		return
	}
	current := currentPrincipal(c)
	grants, err := handler.repository.OAuthGrants(c.Request.Context(), current.User.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load OAuth grants")
		return
	}
	writeData(c, http.StatusOK, grants)
}

func (handler *Handler) oauthRevokeGrant(c *gin.Context) {
	if !handler.requireFeature(c, "oauth") {
		return
	}
	var request struct {
		GrantID string `json:"grantId"`
	}
	if !decode(c, &request) || strings.TrimSpace(request.GrantID) == "" {
		return
	}
	current := currentPrincipal(c)
	if err := handler.repository.RevokeOAuthGrant(c.Request.Context(), request.GrantID, current.User.ID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "OAuth grant not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) oauthToken(c *gin.Context) {
	grantType := strings.TrimSpace(c.PostForm("grant_type"))
	if grantType == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "grant_type is required")
		return
	}
	clientID, clientSecret := oauthClientCredentials(c)
	if clientID == "" {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "client_id is required")
		return
	}
	if grantType == "authorization_code" {
		handler.oauthTokenAuthorizationCode(c, clientID, clientSecret)
		return
	}
	if grantType == "refresh_token" {
		handler.oauthTokenRefresh(c, clientID, clientSecret)
		return
	}
	oauthError(c, http.StatusBadRequest, "unsupported_grant_type", "Unsupported grant type")
}

func (handler *Handler) oauthTokenAuthorizationCode(c *gin.Context, clientID, clientSecret string) {
	codeValue := strings.TrimSpace(c.PostForm("code"))
	redirectURI := strings.TrimSpace(c.PostForm("redirect_uri"))
	if codeValue == "" || redirectURI == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "code and redirect_uri are required")
		return
	}
	client, err := handler.repository.OAuthClientByID(c.Request.Context(), clientID, "")
	if err != nil || !verifyOAuthClientSecret(client, clientSecret) {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "Invalid client credentials")
		return
	}
	code, err := handler.repository.ConsumeOAuthAuthorizationCode(c.Request.Context(), codeValue, client.ID, redirectURI)
	if err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "Authorization code is invalid or expired")
		return
	}
	if method := strings.TrimSpace(valueOrDefault(code.CodeChallengeMethod, "")); code.CodeChallenge != nil && method != "S256" {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "Unsupported code challenge method")
		return
	}
	if code.CodeChallenge != nil && subtle.ConstantTimeCompare([]byte(*code.CodeChallenge), []byte(s256(strings.TrimSpace(c.PostForm("code_verifier"))))) != 1 {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "Invalid code verifier")
		return
	}
	grant, err := handler.repository.UpsertOAuthGrant(c.Request.Context(), code.UserID, client.ID, code.WorkspaceID, code.Scopes)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to create OAuth grant")
		return
	}
	handler.issueOAuthTokenResponse(c, grant, client.ID, code.Scopes)
}

func (handler *Handler) oauthTokenRefresh(c *gin.Context, clientID, clientSecret string) {
	refreshValue := strings.TrimSpace(c.PostForm("refresh_token"))
	if refreshValue == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	token, err := handler.repository.OAuthTokenByRefreshHash(c.Request.Context(), refreshValue)
	if err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "Refresh token is invalid or expired")
		return
	}
	grant, err := handler.repository.OAuthGrantByID(c.Request.Context(), token.GrantID, token.WorkspaceID)
	if err != nil || grant.ClientID != clientID {
		oauthError(c, http.StatusBadRequest, "invalid_grant", "Refresh token is invalid")
		return
	}
	client, err := handler.repository.OAuthClientByID(c.Request.Context(), grant.ClientID, token.WorkspaceID)
	if err != nil || !verifyOAuthClientSecret(client, clientSecret) {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "Invalid client credentials")
		return
	}
	if err := handler.repository.RevokeOAuthToken(c.Request.Context(), token.ID, token.WorkspaceID); err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to rotate refresh token")
		return
	}
	handler.issueOAuthTokenResponse(c, grant, client.ID, token.Scopes)
}

func (handler *Handler) issueOAuthTokenResponse(c *gin.Context, grant postgres.OAuthGrant, clientID string, scopes []string) {
	jti, err := randomSSOValue(24)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to issue access token")
		return
	}
	accessToken, err := handler.tokens.issue(tokenClaims{
		Subject: grant.UserID, WorkspaceID: grant.WorkspaceID, Type: "oauth_access", OAuthGrantID: grant.ID,
		OAuthScope: strings.Join(scopes, " "), Audience: clientID, Issuer: handler.backendURL(c), JTI: jti,
	}, oauthAccessTTL)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to issue access token")
		return
	}
	refreshToken, err := randomSSOValue(48)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to issue refresh token")
		return
	}
	if err := handler.repository.CreateOAuthToken(c.Request.Context(), postgres.OAuthToken{
		GrantID: grant.ID, WorkspaceID: grant.WorkspaceID, AccessTokenJTI: jti,
		RefreshTokenHash: nonEmptyPointer(refreshToken), Scopes: scopes,
		AccessExpiresAt: time.Now().UTC().Add(oauthAccessTTL), RefreshExpiresAt: timePtr(time.Now().UTC().Add(oauthRefreshTTL)),
	}); err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to persist access token")
		return
	}
	// The persistence layer hashes refresh tokens before storing them. The
	// token endpoint still returns the opaque value only once to the client.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"access_token": accessToken, "token_type": "Bearer", "expires_in": int(oauthAccessTTL.Seconds()),
		"refresh_token": refreshToken, "scope": strings.Join(scopes, " "),
	})
}

func (handler *Handler) oauthRegister(c *gin.Context) {
	workspace, err := handler.resolveOAuthWorkspace(c, strings.TrimSpace(c.Query("workspaceId")))
	if err != nil {
		oauthError(c, http.StatusNotFound, "invalid_target", "Workspace not found")
		return
	}
	if !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "oauth") {
		oauthError(c, http.StatusForbidden, "access_denied", "OAuth is not enabled for this workspace")
		return
	}
	var request oauthRegisterRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_client_metadata", "Invalid registration request")
		return
	}
	if strings.TrimSpace(request.ClientName) == "" || len(request.RedirectURIs) == 0 {
		oauthError(c, http.StatusBadRequest, "invalid_client_metadata", "client_name and redirect_uris are required")
		return
	}
	for _, redirectURI := range request.RedirectURIs {
		if !validOAuthRedirectURI(redirectURI) {
			oauthError(c, http.StatusBadRequest, "invalid_redirect_uri", "Invalid redirect URI")
			return
		}
	}
	grantTypes := normalizeOAuthScopes(request.GrantTypes)
	if len(grantTypes) == 0 {
		grantTypes = []string{"authorization_code"}
	}
	if !containsString(grantTypes, "authorization_code") {
		oauthError(c, http.StatusBadRequest, "invalid_client_metadata", "authorization_code grant is required")
		return
	}
	scopes := normalizeOAuthScopes(strings.Fields(request.Scope))
	if len(scopes) == 0 {
		scopes = []string{"read", "write"}
	}
	for _, scope := range scopes {
		if scope != "read" && scope != "write" {
			oauthError(c, http.StatusBadRequest, "invalid_scope", "Unsupported scope")
			return
		}
	}
	authMethod := strings.TrimSpace(request.TokenEndpointAuthMethod)
	if authMethod == "" {
		authMethod = "none"
	}
	if authMethod != "none" && authMethod != "client_secret_basic" && authMethod != "client_secret_post" {
		oauthError(c, http.StatusBadRequest, "invalid_client_metadata", "Unsupported token endpoint auth method")
		return
	}
	secret := ""
	secretHash := ""
	if authMethod != "none" {
		var err error
		secret, err = randomSSOValue(32)
		if err != nil {
			oauthError(c, http.StatusInternalServerError, "server_error", "Failed to generate client secret")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
		if err != nil {
			oauthError(c, http.StatusInternalServerError, "server_error", "Failed to secure client secret")
			return
		}
		secretHash = string(hash)
	}
	client, err := handler.repository.CreateOAuthClient(c.Request.Context(), strings.TrimSpace(request.ClientName), request.RedirectURIs,
		grantTypes, scopes, authMethod, secretHash, workspace.ID, true)
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to register OAuth client")
		return
	}
	response := gin.H{"client_id": client.ID, "client_name": client.Name, "redirect_uris": client.RedirectURIs,
		"grant_types": client.GrantTypes, "response_types": []string{"code"}, "scope": strings.Join(client.Scopes, " "),
		"token_endpoint_auth_method": client.TokenEndpointAuthMethod, "client_id_issued_at": client.CreatedAt.Unix()}
	if secret != "" {
		response["client_secret"] = secret
	}
	c.JSON(http.StatusCreated, response)
}

func (handler *Handler) validateOAuthAuthorize(c *gin.Context, params oauthAuthorizeParams, workspaceID string) (domain.Workspace, postgres.OAuthClient, []string, error) {
	workspace, err := handler.resolveOAuthWorkspace(c, workspaceID)
	if err != nil {
		return domain.Workspace{}, postgres.OAuthClient{}, nil, errors.New("workspace not found")
	}
	client, err := handler.repository.OAuthClientByID(c.Request.Context(), strings.TrimSpace(params.ClientID), workspace.ID)
	if err != nil {
		return workspace, client, nil, errors.New("client not found")
	}
	if params.ResponseType != "code" || params.ClientID == "" || params.RedirectURI == "" {
		return workspace, client, nil, errors.New("response_type, client_id and redirect_uri are required")
	}
	if !containsString(client.RedirectURIs, params.RedirectURI) {
		return workspace, client, nil, errors.New("redirect_uri is not registered")
	}
	if !containsString(client.GrantTypes, "authorization_code") && len(client.GrantTypes) > 0 {
		return workspace, client, nil, errors.New("client does not support authorization_code")
	}
	if params.CodeChallengeMethod != "" && params.CodeChallengeMethod != "S256" {
		return workspace, client, nil, errors.New("only S256 code challenge is supported")
	}
	if client.TokenEndpointAuthMethod == "none" && strings.TrimSpace(params.CodeChallenge) == "" {
		return workspace, client, nil, errors.New("public clients must use PKCE")
	}
	requested := normalizeOAuthScopes(strings.Fields(params.Scope))
	if len(requested) == 0 {
		requested = []string{"read"}
	}
	for _, scope := range requested {
		if !containsString(client.Scopes, scope) || (scope != "read" && scope != "write") {
			return workspace, client, nil, errors.New("unsupported scope")
		}
	}
	return workspace, client, requested, nil
}

// OAuthWorkspace is intentionally small and local to the OAuth adapter; it
// avoids exposing the full domain workspace as part of the repository API.
// The alias is backed by the same fields needed by the handler.
func (handler *Handler) resolveOAuthWorkspace(c *gin.Context, workspaceID string) (domain.Workspace, error) {
	if strings.TrimSpace(workspaceID) != "" {
		return handler.repository.WorkspaceByID(c.Request.Context(), strings.TrimSpace(workspaceID))
	}
	return handler.repository.OnlyWorkspace(c.Request.Context())
}

func oauthAuthorizeParamsFromValues(values url.Values) oauthAuthorizeParams {
	return oauthAuthorizeParams{ResponseType: values.Get("response_type"), ClientID: values.Get("client_id"), RedirectURI: values.Get("redirect_uri"), State: values.Get("state"), CodeChallenge: values.Get("code_challenge"), CodeChallengeMethod: values.Get("code_challenge_method"), Scope: values.Get("scope"), Resource: values.Get("resource")}
}

func (handler *Handler) writeOAuthAuthorizeError(c *gin.Context, params oauthAuthorizeParams, err error) {
	if params.RedirectURI != "" {
		if client, clientErr := handler.repository.OAuthClientByID(c.Request.Context(), params.ClientID, ""); clientErr == nil && containsString(client.RedirectURIs, params.RedirectURI) {
			c.Redirect(http.StatusFound, oauthRedirectError(params.RedirectURI, params.State, "invalid_request"))
			return
		}
	}
	oauthError(c, http.StatusBadRequest, "invalid_request", err.Error())
}

func oauthClientCredentials(c *gin.Context) (string, string) {
	if id, secret, ok := c.Request.BasicAuth(); ok {
		return strings.TrimSpace(id), secret
	}
	return strings.TrimSpace(c.PostForm("client_id")), c.PostForm("client_secret")
}

func verifyOAuthClientSecret(client postgres.OAuthClient, secret string) bool {
	if client.TokenEndpointAuthMethod == "none" {
		return strings.TrimSpace(secret) == ""
	}
	return client.SecretHash != nil && bcrypt.CompareHashAndPassword([]byte(*client.SecretHash), []byte(secret)) == nil
}

func normalizeOAuthScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validOAuthRedirectURI(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Fragment == ""
}

func oauthRedirectCode(redirectURI, state, code string) string {
	return oauthRedirect(redirectURI, state, url.Values{"code": []string{code}})
}

func oauthRedirectError(redirectURI, state, code string) string {
	return oauthRedirect(redirectURI, state, url.Values{"error": []string{code}})
}

func oauthRedirect(redirectURI, state string, values url.Values) string {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	query := parsed.Query()
	for key, valuesForKey := range values {
		for _, value := range valuesForKey {
			query.Set(key, value)
		}
	}
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func oauthError(c *gin.Context, status int, code, description string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": code, "error_description": description})
}

func timePtr(value time.Time) *time.Time { return &value }
