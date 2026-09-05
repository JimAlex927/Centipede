package http

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const mfaChallengeTTL = 10 * time.Minute

type mfaPrincipal struct {
	Principal principal
	Transfer  bool
}

type mfaRequest struct {
	Method           string `json:"method"`
	VerificationCode string `json:"verificationCode"`
	ConfirmPassword  string `json:"confirmPassword"`
	Code             string `json:"code"`
}

func (handler *Handler) mfaStatus(c *gin.Context) {
	if !handler.requireFeatureForMFA(c) {
		return
	}
	current, ok := handler.mfaPrincipal(c)
	if !ok {
		return
	}
	record, err := handler.repository.MFAByUser(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		writeData(c, http.StatusOK, gin.H{"isEnabled": false, "method": nil, "backupCodesCount": 0})
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load MFA status")
		return
	}
	writeData(c, http.StatusOK, gin.H{
		"isEnabled": record.IsEnabled, "method": record.Method,
		"backupCodesCount": len(record.BackupCodes),
	})
}

func (handler *Handler) setupMFA(c *gin.Context) {
	if !handler.requireFeatureForMFA(c) {
		return
	}
	current, ok := handler.mfaPrincipal(c)
	if !ok {
		return
	}
	var request mfaRequest
	if !decode(c, &request) {
		return
	}
	method := strings.ToLower(strings.TrimSpace(request.Method))
	if method == "" {
		method = "totp"
	}
	if method != "totp" {
		writeError(c, http.StatusBadRequest, "Only TOTP MFA is supported")
		return
	}
	if existing, err := handler.repository.MFAByUser(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID); err == nil && existing.IsEnabled {
		writeError(c, http.StatusBadRequest, "MFA is already enabled")
		return
	}
	secret, err := newMFASecret()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to generate MFA secret")
		return
	}
	if _, err = handler.repository.UpsertMFASetup(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID, method, secret); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to save MFA setup")
		return
	}
	issuer := "Docmost"
	label := issuer + ":" + current.Principal.User.Email
	uri := "otpauth://totp/" + url.PathEscape(label) + "?" + url.Values{
		"secret": []string{secret}, "issuer": []string{issuer}, "algorithm": []string{"SHA1"}, "digits": []string{"6"}, "period": []string{"30"},
	}.Encode()
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to generate MFA QR code")
		return
	}
	writeData(c, http.StatusOK, gin.H{
		"method":    method,
		"qrCode":    "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		"manualKey": secret,
	})
}

func (handler *Handler) enableMFA(c *gin.Context) {
	if !handler.requireFeatureForMFA(c) {
		return
	}
	current, ok := handler.mfaPrincipal(c)
	if !ok {
		return
	}
	var request mfaRequest
	if !decode(c, &request) {
		return
	}
	record, err := handler.repository.MFAByUser(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID)
	if err != nil || record.Secret == nil || record.Method != "totp" {
		writeError(c, http.StatusBadRequest, "MFA setup has not been started")
		return
	}
	if !verifyTOTP(*record.Secret, request.VerificationCode, time.Now().UTC()) {
		writeError(c, http.StatusBadRequest, "Invalid verification code")
		return
	}
	backupCodes, storedCodes, err := generateBackupCodes(8)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to generate backup codes")
		return
	}
	if err = handler.repository.EnableMFA(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID, storedCodes); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to enable MFA")
		return
	}
	actorID := current.Principal.User.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Principal.Workspace.ID, ActorID: &actorID, Event: "user.mfa_enabled", ResourceType: "user", ResourceID: &actorID,
	})
	if current.Transfer {
		if err = handler.startSession(c, current.Principal.User); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to create login session")
			return
		}
	}
	writeData(c, http.StatusOK, gin.H{"success": true, "backupCodes": backupCodes})
}

func (handler *Handler) disableMFA(c *gin.Context) {
	if !handler.requireFeatureForMFA(c) {
		return
	}
	current, ok := handler.mfaPrincipal(c)
	if !ok || current.Transfer {
		return
	}
	var request mfaRequest
	if !decodeOptional(c, &request) {
		return
	}
	if !handler.confirmMFAPassword(current.Principal.User, request.ConfirmPassword) {
		writeError(c, http.StatusBadRequest, "Password is required to disable MFA")
		return
	}
	if err := handler.repository.DisableMFA(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to disable MFA")
		return
	}
	actorID := current.Principal.User.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Principal.Workspace.ID, ActorID: &actorID, Event: "user.mfa_disabled", ResourceType: "user", ResourceID: &actorID,
	})
	writeData(c, http.StatusOK, gin.H{"success": true})
}

func (handler *Handler) generateMFABackupCodes(c *gin.Context) {
	if !handler.requireFeatureForMFA(c) {
		return
	}
	current, ok := handler.mfaPrincipal(c)
	if !ok || current.Transfer {
		return
	}
	var request mfaRequest
	if !decodeOptional(c, &request) {
		return
	}
	if !handler.confirmMFAPassword(current.Principal.User, request.ConfirmPassword) {
		writeError(c, http.StatusBadRequest, "Password is required to generate backup codes")
		return
	}
	record, err := handler.repository.MFAByUser(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID)
	if err != nil || !record.IsEnabled {
		writeError(c, http.StatusBadRequest, "MFA is not enabled")
		return
	}
	plainCodes, storedCodes, err := generateBackupCodes(8)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to generate backup codes")
		return
	}
	if err = handler.repository.ReplaceMFABackupCodes(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID, storedCodes); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to save backup codes")
		return
	}
	writeData(c, http.StatusOK, gin.H{"backupCodes": plainCodes})
}

func (handler *Handler) verifyMFA(c *gin.Context) {
	if !handler.requireFeatureForMFA(c) {
		return
	}
	current, ok := handler.mfaPrincipal(c)
	if !ok || !current.Transfer {
		if ok {
			writeError(c, http.StatusBadRequest, "MFA verification is not required")
		}
		return
	}
	var request mfaRequest
	if !decode(c, &request) {
		return
	}
	record, err := handler.repository.MFAByUser(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID)
	if err != nil || !record.IsEnabled || record.Secret == nil {
		writeError(c, http.StatusUnauthorized, "MFA is not configured")
		return
	}
	code := strings.TrimSpace(request.Code)
	valid := verifyTOTP(*record.Secret, code, time.Now().UTC())
	if !valid {
		normalized := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(code, "-", ""), " ", ""))
		valid, err = handler.repository.ConsumeMFABackupCode(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID, hashBackupCode(normalized))
		if !valid && err == nil {
			// Keep compatibility with installations that predate hashed backup codes.
			valid, err = handler.repository.ConsumeMFABackupCode(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID, normalized)
		}
	}
	if err != nil || !valid {
		writeError(c, http.StatusBadRequest, "Invalid MFA code")
		return
	}
	if err = handler.startSession(c, current.Principal.User); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
		return
	}
	writeData(c, http.StatusOK, gin.H{"success": true})
}

func (handler *Handler) validateMFAAccess(c *gin.Context) {
	current, ok := handler.mfaPrincipal(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "Unauthorized")
		return
	}
	record, err := handler.repository.MFAByUser(c.Request.Context(), current.Principal.User.ID, current.Principal.Workspace.ID)
	userHasMFA := err == nil && record.IsEnabled
	writeData(c, http.StatusOK, gin.H{
		"valid": true, "isTransferToken": current.Transfer,
		"requiresMfaSetup": current.Transfer && current.Principal.Workspace.EnforceMFA && !userHasMFA,
		"userHasMfa":       userHasMFA, "isMfaEnforced": current.Principal.Workspace.EnforceMFA,
	})
}

func (handler *Handler) mfaPrincipal(c *gin.Context) (mfaPrincipal, bool) {
	raw := requestToken(c)
	if raw == "" {
		writeError(c, http.StatusUnauthorized, "Unauthorized")
		return mfaPrincipal{}, false
	}
	if claims, err := handler.tokens.parse(raw, "access"); err == nil {
		active, sessionErr := handler.repository.SessionActive(c.Request.Context(), claims.SessionID, claims.Subject, claims.WorkspaceID)
		if sessionErr == nil && active {
			return mfaPrincipal{Principal: handler.loadPrincipal(c, claims.Subject, claims.WorkspaceID, claims.SessionID), Transfer: false}, true
		}
	}
	claims, err := handler.tokens.parse(raw, "mfa")
	if err != nil {
		writeError(c, http.StatusUnauthorized, "Unauthorized")
		return mfaPrincipal{}, false
	}
	user, userErr := handler.repository.UserByID(c.Request.Context(), claims.Subject, claims.WorkspaceID)
	workspace, workspaceErr := handler.repository.WorkspaceByID(c.Request.Context(), claims.WorkspaceID)
	if userErr != nil || workspaceErr != nil || user.DeactivatedAt != nil || user.DeletedAt != nil {
		writeError(c, http.StatusUnauthorized, "Unauthorized")
		return mfaPrincipal{}, false
	}
	return mfaPrincipal{Principal: principal{User: user, Workspace: workspace}, Transfer: true}, true
}

func (handler *Handler) loadPrincipal(c *gin.Context, userID, workspaceID, sessionID string) principal {
	user, _ := handler.repository.UserByID(c.Request.Context(), userID, workspaceID)
	workspace, _ := handler.repository.WorkspaceByID(c.Request.Context(), workspaceID)
	return principal{User: user, Workspace: workspace, SessionID: sessionID}
}

func (handler *Handler) issueMFAChallenge(c *gin.Context, user domain.User) (string, error) {
	if user.WorkspaceID == nil {
		return "", fmt.Errorf("user has no workspace")
	}
	token, err := handler.tokens.issue(tokenClaims{Subject: user.ID, Email: user.Email, WorkspaceID: *user.WorkspaceID, Type: "mfa"}, mfaChallengeTTL)
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().UTC().Add(mfaChallengeTTL)
	http.SetCookie(c.Writer, &http.Cookie{Name: "authToken", Value: token, Path: "/", Expires: expiresAt, MaxAge: int(mfaChallengeTTL.Seconds()), HttpOnly: true, Secure: handler.cookieSecure, SameSite: http.SameSiteLaxMode})
	return token, nil
}

func (handler *Handler) requireFeatureForMFA(c *gin.Context) bool {
	current, ok := handler.mfaPrincipal(c)
	if !ok {
		return false
	}
	c.Set(principalKey, current.Principal)
	return handler.requireFeature(c, "mfa")
}

func (handler *Handler) confirmMFAPassword(user domain.User, password string) bool {
	if user.HasGeneratedPassword {
		return true
	}
	return user.Password != nil && bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(password)) == nil
}

func requestToken(c *gin.Context) string {
	if cookie, err := c.Request.Cookie("authToken"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	header := c.GetHeader("Authorization")
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	return ""
}

func newMFASecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func verifyTOTP(secret, input string, now time.Time) bool {
	input = strings.TrimSpace(input)
	if len(input) != 6 {
		return false
	}
	for _, offset := range []int64{-1, 0, 1} {
		if hmac.Equal([]byte(totpCode(secret, now.Unix()/30+offset)), []byte(input)) {
			return true
		}
	}
	return false
}

func totpCode(secret string, counter int64) string {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))
	digest := hmac.New(sha1.New, decoded)
	_, _ = digest.Write(message[:])
	sum := digest.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1000000)
}

func generateBackupCodes(count int) ([]string, []string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	plain := make([]string, 0, count)
	stored := make([]string, 0, count)
	for i := 0; i < count; i++ {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		var builder strings.Builder
		for _, value := range raw {
			builder.WriteByte(alphabet[int(value)%len(alphabet)])
		}
		code := builder.String()
		plain = append(plain, code)
		stored = append(stored, hashBackupCode(code))
	}
	return plain, stored, nil
}

func hashBackupCode(code string) string {
	digest := sha256.Sum256([]byte(code))
	return "sha256:" + hex.EncodeToString(digest[:])
}
