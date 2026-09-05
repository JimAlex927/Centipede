package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type tokenClaims struct {
	Subject      string `json:"sub"`
	Email        string `json:"email,omitempty"`
	AttachmentID string `json:"attachmentId,omitempty"`
	PageID       string `json:"pageId,omitempty"`
	WorkspaceID  string `json:"workspaceId"`
	Type         string `json:"type"`
	SessionID    string `json:"sessionId,omitempty"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
}

type tokenService struct {
	secret []byte
}

func newTokenService(secret string) *tokenService {
	return &tokenService{secret: []byte(secret)}
}

func (service *tokenService) issue(claims tokenClaims, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = now.Add(ttl).Unix()
	header, err := encodeTokenPart(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := encodeTokenPart(claims)
	if err != nil {
		return "", err
	}
	unsigned := header + "." + payload
	return unsigned + "." + service.sign(unsigned), nil
}

func (service *tokenService) parse(raw, expectedType string) (tokenClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return tokenClaims{}, errors.New("invalid token")
	}
	if !hmac.Equal([]byte(service.sign(parts[0]+"."+parts[1])), []byte(parts[2])) {
		return tokenClaims{}, errors.New("invalid token")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := decodeTokenPart(parts[0], &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return tokenClaims{}, errors.New("invalid token")
	}
	var claims tokenClaims
	if err := decodeTokenPart(parts[1], &claims); err != nil {
		return tokenClaims{}, errors.New("invalid token")
	}
	now := time.Now().UTC().Unix()
	if claims.WorkspaceID == "" || claims.Type != expectedType || claims.ExpiresAt <= now || claims.IssuedAt > now+60 {
		return tokenClaims{}, errors.New("invalid token")
	}
	if (claims.Type == "access" || claims.Type == "collab") && claims.Subject == "" {
		return tokenClaims{}, errors.New("invalid token")
	}
	// Access tokens must participate in session revocation. Other token types
	// (for example collaboration) have their own lifetime and validation flow.
	if claims.Type == "access" && claims.SessionID == "" {
		return tokenClaims{}, errors.New("invalid token")
	}
	return claims, nil
}

func (service *tokenService) sign(unsigned string) string {
	digest := hmac.New(sha256.New, service.secret)
	_, _ = digest.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func encodeTokenPart(value any) (string, error) {
	contents, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(contents), nil
}

func decodeTokenPart(raw string, destination any) error {
	contents, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, destination)
}
