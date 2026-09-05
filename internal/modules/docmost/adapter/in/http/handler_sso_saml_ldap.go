package http

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/crewjam/saml"
	"github.com/gin-gonic/gin"
	"github.com/go-ldap/ldap/v3"
)

func (handler *Handler) samlLogin(c *gin.Context) {
	providerID := strings.TrimSpace(c.Param("providerID"))
	workspace, provider, ok := handler.ssoProvider(c, providerID, "saml")
	if !ok {
		return
	}
	if provider.SAMLURL == nil || provider.SAMLCertificate == nil {
		writeError(c, http.StatusBadRequest, "SAML provider is not configured")
		return
	}

	certificate, certData, err := parseSAMLSigningCertificate(*provider.SAMLCertificate)
	if err != nil {
		writeError(c, http.StatusBadRequest, "SAML certificate is invalid")
		return
	}
	idpURL, err := validateSSOURL(*provider.SAMLURL)
	if err != nil {
		writeError(c, http.StatusBadRequest, "SAML login URL is invalid")
		return
	}
	entityID := handler.backendURL(c) + "/api/sso/saml/" + url.PathEscape(provider.ID) + "/login"
	callbackURL := handler.backendURL(c) + "/api/sso/saml/" + url.PathEscape(provider.ID) + "/callback"
	serviceProvider, err := newSAMLServiceProvider(entityID, callbackURL, idpURL.String(), entityID, certificate, certData)
	if err != nil {
		writeError(c, http.StatusBadRequest, "SAML provider is invalid")
		return
	}
	authnRequest, err := serviceProvider.MakeAuthenticationRequest(idpURL.String(), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		writeError(c, http.StatusBadGateway, "Failed to create SAML request")
		return
	}
	state, err := handler.tokens.issue(tokenClaims{
		WorkspaceID: workspace.ID, Type: "sso_state", ProviderID: provider.ID,
		Redirect: safeSSORedirect(c.Query("redirect")), SAMLRequestID: authnRequest.ID,
	}, ssoStateTTL)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SSO state")
		return
	}
	redirectURL, err := authnRequest.Redirect(state, serviceProvider)
	if err != nil {
		writeError(c, http.StatusBadGateway, "Failed to create SAML redirect")
		return
	}
	c.Redirect(http.StatusFound, redirectURL.String())
}

func (handler *Handler) samlCallback(c *gin.Context) {
	state, err := handler.tokens.parse(strings.TrimSpace(c.PostForm("RelayState")), "sso_state")
	if err != nil || state.ProviderID == "" || state.ProviderID != strings.TrimSpace(c.Param("providerID")) || state.SAMLRequestID == "" {
		writeError(c, http.StatusBadRequest, "Invalid SSO state")
		return
	}
	workspace, err := handler.repository.WorkspaceByID(c.Request.Context(), state.WorkspaceID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Workspace not found")
		return
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), state.ProviderID, workspace.ID)
	if err != nil || provider.Type != "saml" || !provider.IsEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "sso:custom") {
		writeError(c, http.StatusNotFound, "SSO provider not found")
		return
	}
	if provider.SAMLURL == nil || provider.SAMLCertificate == nil {
		writeError(c, http.StatusBadRequest, "SAML provider is not configured")
		return
	}
	certificate, certData, err := parseSAMLSigningCertificate(*provider.SAMLCertificate)
	if err != nil {
		writeError(c, http.StatusBadRequest, "SAML certificate is invalid")
		return
	}
	idpURL, err := validateSSOURL(*provider.SAMLURL)
	if err != nil {
		writeError(c, http.StatusBadRequest, "SAML login URL is invalid")
		return
	}
	responseXML, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.PostForm("SAMLResponse")))
	if err != nil || len(responseXML) == 0 {
		writeError(c, http.StatusBadRequest, "SAML response is invalid")
		return
	}
	issuer, err := samlResponseIssuer(responseXML)
	if err != nil || issuer == "" {
		writeError(c, http.StatusUnauthorized, "SAML response issuer is missing")
		return
	}
	entityID := handler.backendURL(c) + "/api/sso/saml/" + url.PathEscape(provider.ID) + "/login"
	callbackURL := handler.backendURL(c) + "/api/sso/saml/" + url.PathEscape(provider.ID) + "/callback"
	serviceProvider, err := newSAMLServiceProvider(entityID, callbackURL, idpURL.String(), issuer, certificate, certData)
	if err != nil {
		writeError(c, http.StatusBadRequest, "SAML provider is invalid")
		return
	}
	if err := c.Request.ParseForm(); err != nil {
		writeError(c, http.StatusBadRequest, "Invalid SAML form")
		return
	}
	assertion, err := serviceProvider.ParseResponse(c.Request, []string{state.SAMLRequestID})
	if err != nil {
		writeError(c, http.StatusUnauthorized, "SAML response could not be verified")
		return
	}
	providerUserID, email, name, groups := samlIdentityWithGroups(assertion)
	if providerUserID == "" || email == "" {
		writeError(c, http.StatusUnauthorized, "SAML identity is missing required attributes")
		return
	}
	user, err := handler.repository.UserByAuthAccount(c.Request.Context(), provider.ID, providerUserID, workspace.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		user, err = handler.repository.CreateSSOUser(c.Request.Context(), workspace.ID, provider.ID, providerUserID, name, email, workspace.DefaultRole, provider.AllowSignup)
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
		if err := handler.repository.SyncSSOGroups(c.Request.Context(), user.ID, workspace.ID, groups); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to synchronize SSO groups")
			return
		}
	}
	if err := handler.finishSSOLogin(c, user, workspace, state.Redirect); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
	}
}

func (handler *Handler) ldapLogin(c *gin.Context) {
	providerID := strings.TrimSpace(c.Param("providerID"))
	workspace, provider, ok := handler.ssoProvider(c, providerID, "ldap")
	if !ok {
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.Username) == "" || request.Password == "" {
		writeError(c, http.StatusBadRequest, "Username and password are required")
		return
	}
	identity, err := authenticateLDAP(c, provider, request.Username, request.Password)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "LDAP authentication failed")
		return
	}
	user, err := handler.repository.UserByAuthAccount(c.Request.Context(), provider.ID, identity.ProviderUserID, workspace.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		user, err = handler.repository.CreateSSOUser(c.Request.Context(), workspace.ID, provider.ID, identity.ProviderUserID, identity.Name, identity.Email, workspace.DefaultRole, provider.AllowSignup)
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
		if err := handler.repository.SyncSSOGroups(c.Request.Context(), user.ID, workspace.ID, identity.Groups); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to synchronize SSO groups")
			return
		}
	}
	if err := handler.finishSSOAPILogin(c, user, workspace); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
	}
}

func (handler *Handler) ssoProvider(c *gin.Context, providerID, expectedType string) (domain.Workspace, postgres.AuthProvider, bool) {
	workspace, err := handler.resolveSSOWorkspace(c, c.Query("workspaceId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "Workspace not found")
		return domain.Workspace{}, postgres.AuthProvider{}, false
	}
	provider, err := handler.repository.AuthProviderByID(c.Request.Context(), providerID, workspace.ID)
	if err != nil || provider.Type != expectedType || !provider.IsEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "sso:custom") {
		writeError(c, http.StatusNotFound, "SSO provider not found")
		return domain.Workspace{}, postgres.AuthProvider{}, false
	}
	return workspace, provider, true
}

func (handler *Handler) finishSSOAPILogin(c *gin.Context, user domain.User, workspace domain.Workspace) error {
	if err := handler.repository.MarkLogin(c.Request.Context(), user.ID, workspace.ID); err != nil {
		return err
	}
	mfaRecord, mfaErr := handler.repository.MFAByUser(c.Request.Context(), user.ID, workspace.ID)
	if mfaErr != nil && !errors.Is(mfaErr, postgres.ErrNotFound) {
		return mfaErr
	}
	if (mfaErr == nil && mfaRecord.IsEnabled) || (workspace.EnforceMFA && (mfaErr != nil || !mfaRecord.IsEnabled)) {
		challengeToken, err := handler.issueMFAChallenge(c, user)
		if err != nil {
			return err
		}
		writeData(c, http.StatusOK, gin.H{
			"userHasMfa":       mfaErr == nil && mfaRecord.IsEnabled,
			"requiresMfaSetup": workspace.EnforceMFA && (mfaErr != nil || !mfaRecord.IsEnabled),
			"isMfaEnforced":    workspace.EnforceMFA, "mfaToken": challengeToken,
		})
		return nil
	}
	if err := handler.startSession(c, user); err != nil {
		return err
	}
	writeData(c, http.StatusOK, nil)
	return nil
}

func validateSSOURL(raw string) (url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return url.URL{}, errors.New("invalid URL")
	}
	return *parsed, nil
}

func parseSAMLSigningCertificate(raw string) (*x509.Certificate, string, error) {
	value := strings.TrimSpace(raw)
	block, _ := pem.Decode([]byte(value))
	der := []byte(nil)
	if block != nil {
		der = block.Bytes
	} else {
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
		if err != nil {
			return nil, "", err
		}
		der = decoded
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", err
	}
	return certificate, base64.StdEncoding.EncodeToString(der), nil
}

func newSAMLServiceProvider(entityID, callbackURL, idpURL, idpEntityID string, certificate *x509.Certificate, certData string) (*saml.ServiceProvider, error) {
	acs, err := url.Parse(callbackURL)
	if err != nil {
		return nil, err
	}
	return &saml.ServiceProvider{
		EntityID: entityID, AcsURL: *acs,
		IDPMetadata: &saml.EntityDescriptor{
			EntityID: idpEntityID,
			IDPSSODescriptors: []saml.IDPSSODescriptor{{
				SSODescriptor: saml.SSODescriptor{RoleDescriptor: saml.RoleDescriptor{KeyDescriptors: []saml.KeyDescriptor{{
					Use: "signing", KeyInfo: saml.KeyInfo{X509Data: saml.X509Data{X509Certificates: []saml.X509Certificate{{Data: certData}}}},
				}}}},
				SingleSignOnServices: []saml.Endpoint{{Binding: saml.HTTPRedirectBinding, Location: idpURL}},
			}},
		},
		// The IdP certificate is used for verification. Requests use the
		// redirect binding and therefore do not need an SP private key.
		Certificate: certificate,
	}, nil
}

func samlResponseIssuer(data []byte) (string, error) {
	var response struct {
		Issuer struct {
			Value string `xml:",chardata"`
		} `xml:"Issuer"`
	}
	if err := xml.Unmarshal(data, &response); err != nil {
		return "", err
	}
	return strings.TrimSpace(response.Issuer.Value), nil
}

func samlIdentity(assertion *saml.Assertion) (providerUserID, email, name string) {
	providerUserID, email, name, _ = samlIdentityWithGroups(assertion)
	return providerUserID, email, name
}

func firstIdentityValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[strings.ToLower(key)]); value != "" {
			return value
		}
	}
	return ""
}

type ldapIdentity struct {
	ProviderUserID string
	Email          string
	Name           string
	Groups         []string
}

func authenticateLDAP(c *gin.Context, provider postgres.AuthProvider, username, password string) (ldapIdentity, error) {
	if provider.LDAPURL == nil || provider.LDAPBaseDN == nil {
		return ldapIdentity{}, errors.New("LDAP provider is not configured")
	}
	parsed, err := url.Parse(strings.TrimSpace(*provider.LDAPURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ldap" && parsed.Scheme != "ldaps") {
		return ldapIdentity{}, errors.New("invalid LDAP URL")
	}
	tlsConfig, err := ldapTLSConfig(parsed, provider)
	if err != nil {
		return ldapIdentity{}, err
	}
	dialOptions := []ldap.DialOpt{ldap.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second})}
	if parsed.Scheme == "ldaps" {
		dialOptions = append(dialOptions, ldap.DialWithTLSConfig(tlsConfig))
	}
	connection, err := ldap.DialURL(parsed.String(), dialOptions...)
	if err != nil {
		return ldapIdentity{}, err
	}
	defer connection.Close()
	connection.SetTimeout(10 * time.Second)
	if parsed.Scheme == "ldap" && provider.LDAPTLSEnabled {
		if err := connection.StartTLS(tlsConfig); err != nil {
			return ldapIdentity{}, err
		}
	}
	bindDN, bindPassword := "", ""
	if provider.LDAPBindDN != nil {
		bindDN = strings.TrimSpace(*provider.LDAPBindDN)
	}
	if provider.LDAPBindPassword != nil {
		bindPassword = *provider.LDAPBindPassword
	}
	if err := connection.Bind(bindDN, bindPassword); err != nil {
		return ldapIdentity{}, err
	}
	filter := ldapSearchFilter(provider.LDAPUserSearchFilter, username)
	attributes := ldapSearchAttributes(provider.LDAPUserAttributes)
	result, err := connection.Search(ldap.NewSearchRequest(strings.TrimSpace(*provider.LDAPBaseDN), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 0, false, filter, attributes, nil))
	if err != nil || len(result.Entries) != 1 {
		return ldapIdentity{}, errors.New("LDAP user not found")
	}
	entry := result.Entries[0]
	if err := connection.Bind(entry.DN, password); err != nil {
		return ldapIdentity{}, err
	}
	email := ldapEntryValue(entry, provider.LDAPUserAttributes, "email", "mail", "userPrincipalName", "emailAddress")
	if email == "" || !strings.Contains(email, "@") {
		return ldapIdentity{}, errors.New("LDAP user has no valid email")
	}
	name := ldapEntryValue(entry, provider.LDAPUserAttributes, "name", "displayName", "cn")
	if name == "" {
		name = strings.TrimSpace(ldapEntryValue(entry, provider.LDAPUserAttributes, "givenName") + " " + ldapEntryValue(entry, provider.LDAPUserAttributes, "sn"))
	}
	providerUserID := ldapEntryValue(entry, provider.LDAPUserAttributes, "id", "uid", "sAMAccountName", "userPrincipalName")
	if providerUserID == "" {
		providerUserID = entry.DN
	}
	groups := append(entry.GetAttributeValues("memberOf"), entry.GetAttributeValues("groups")...)
	return ldapIdentity{ProviderUserID: providerUserID, Email: email, Name: name, Groups: normalizeSSOGroupNames(groups)}, nil
}

func ldapSearchFilter(configured *string, username string) string {
	filter := "(mail={{username}})"
	if configured != nil && strings.TrimSpace(*configured) != "" {
		filter = strings.TrimSpace(*configured)
	}
	return strings.ReplaceAll(filter, "{{username}}", ldap.EscapeFilter(strings.TrimSpace(username)))
}

func ldapTLSConfig(parsed *url.URL, provider postgres.AuthProvider) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()}
	if provider.LDAPTLSCACert == nil || strings.TrimSpace(*provider.LDAPTLSCACert) == "" {
		return config, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(*provider.LDAPTLSCACert)) {
		return nil, errors.New("invalid LDAP CA certificate")
	}
	config.RootCAs = pool
	return config, nil
}

func ldapSearchAttributes(raw json.RawMessage) []string {
	attributes := []string{"mail", "email", "emailAddress", "userPrincipalName", "uid", "sAMAccountName", "displayName", "cn", "givenName", "sn", "memberOf", "groups"}
	var mapping map[string]any
	if json.Unmarshal(raw, &mapping) == nil {
		for _, value := range mapping {
			switch typed := value.(type) {
			case string:
				attributes = append(attributes, typed)
			case []any:
				for _, candidate := range typed {
					if value, ok := candidate.(string); ok {
						attributes = append(attributes, value)
					}
				}
			}
		}
	}
	return uniqueLDAPAttributes(attributes)
}

func uniqueLDAPAttributes(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[strings.ToLower(value)] {
			seen[strings.ToLower(value)] = true
			result = append(result, value)
		}
	}
	return result
}

func ldapEntryValue(entry *ldap.Entry, mapping json.RawMessage, keys ...string) string {
	var configured map[string]any
	_ = json.Unmarshal(mapping, &configured)
	for _, key := range keys {
		candidateNames := []string{key}
		if configuredValue, ok := configured[key]; ok {
			switch typed := configuredValue.(type) {
			case string:
				candidateNames = append([]string{typed}, candidateNames...)
			case []any:
				for _, candidate := range typed {
					if value, ok := candidate.(string); ok {
						candidateNames = append([]string{value}, candidateNames...)
					}
				}
			}
		}
		for _, name := range candidateNames {
			if value := strings.TrimSpace(entry.GetAttributeValue(name)); value != "" {
				return value
			}
		}
	}
	return ""
}
