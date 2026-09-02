package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"centipede/internal/modules/identity/domain"
)

type Service struct {
	secret []byte
	issuer string
}

func New(secret, issuer string) *Service { return &Service{secret: []byte(secret), issuer: issuer} }

func (service *Service) IssueAccessToken(user domain.User, expiresAt time.Time) (string, error) {
	header, err := encode(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := encode(map[string]any{
		"sub": strconv.FormatInt(user.ID, 10), "iss": service.issuer,
		"iat": time.Now().UTC().Unix(), "exp": expiresAt.Unix(), "typ": "access",
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + payload
	return unsigned + "." + service.sign(unsigned), nil
}

func (service *Service) ParseAccessToken(raw string) (int64, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return 0, errors.New("invalid access token")
	}
	expected := service.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return 0, errors.New("invalid access token")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := decode(parts[0], &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return 0, errors.New("invalid access token")
	}
	var claims struct {
		Subject string `json:"sub"`
		Issuer  string `json:"iss"`
		Issued  int64  `json:"iat"`
		Expiry  int64  `json:"exp"`
		Type    string `json:"typ"`
	}
	if err := decode(parts[1], &claims); err != nil || claims.Subject == "" || claims.Issuer != service.issuer || claims.Type != "access" {
		return 0, errors.New("invalid access token")
	}
	now := time.Now().UTC().Unix()
	if claims.Expiry <= now || claims.Issued > now+60 {
		return 0, errors.New("invalid access token")
	}
	return strconv.ParseInt(claims.Subject, 10, 64)
}

func (service *Service) sign(unsigned string) string {
	hash := hmac.New(sha256.New, service.secret)
	_, _ = hash.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func encode(value any) (string, error) {
	contents, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(contents), nil
}

func decode(raw string, destination any) error {
	contents, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, destination)
}
