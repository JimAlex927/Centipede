package enterprise

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	LicenseTypeBusiness   = "business"
	LicenseTypeEnterprise = "enterprise"
)

// Features are kept in one place so the API, CLI and frontend entitlement
// response cannot silently drift apart.
var AllFeatures = []string{
	"sso:custom", "sso:google", "mfa", "api:keys", "comment:resolution",
	"page:permissions", "ai", "import:confluence", "import:docx", "import:pdf",
	"attachment:indexing", "security:settings", "mcp", "scim", "page:verification",
	"audit:logs", "retention", "sharing:controls", "templates", "comment:viewer",
	"spaces:personal", "export:docx", "bases", "oauth", "ai:controls", "mcp:controls",
}

type License struct {
	ID           string    `json:"id"`
	CustomerName string    `json:"customerName"`
	SeatCount    int       `json:"seatCount"`
	LicenseType  string    `json:"licenseType"`
	IssuedAt     time.Time `json:"issuedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Trial        bool      `json:"trial"`
	Features     []string  `json:"features"`
	WorkspaceID  string    `json:"workspaceId,omitempty"`
}

type GenerateInput struct {
	ID           string
	CustomerName string
	SeatCount    int
	LicenseType  string
	IssuedAt     time.Time
	ExpiresAt    time.Time
	Trial        bool
	Features     []string
	WorkspaceID  string
}

type licenseClaims struct {
	ID           string   `json:"id"`
	CustomerName string   `json:"customerName"`
	SeatCount    int      `json:"seatCount"`
	LicenseType  string   `json:"licenseType"`
	IssuedAt     int64    `json:"iat"`
	ExpiresAt    int64    `json:"exp"`
	Trial        bool     `json:"trial"`
	Features     []string `json:"features,omitempty"`
	WorkspaceID  string   `json:"workspaceId,omitempty"`
}

type tokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type Service struct{ secret []byte }

func NewLicenseService(secret string) *Service { return &Service{secret: []byte(secret)} }

func (service *Service) Generate(input GenerateInput) (string, License, error) {
	now := time.Now().UTC()
	if input.IssuedAt.IsZero() {
		input.IssuedAt = now
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = input.IssuedAt.AddDate(1, 0, 0)
	}
	if input.ID == "" {
		var err error
		input.ID, err = newID()
		if err != nil {
			return "", License{}, err
		}
	}
	if len(input.Features) == 0 {
		input.Features = append([]string(nil), AllFeatures...)
	}
	license := License{
		ID: input.ID, CustomerName: strings.TrimSpace(input.CustomerName), SeatCount: input.SeatCount,
		LicenseType: strings.ToLower(strings.TrimSpace(input.LicenseType)), IssuedAt: input.IssuedAt.UTC(),
		ExpiresAt: input.ExpiresAt.UTC(), Trial: input.Trial, Features: normalizeFeatures(input.Features),
		WorkspaceID: strings.TrimSpace(input.WorkspaceID),
	}
	if err := validateLicense(license, now, false); err != nil {
		return "", License{}, err
	}
	token, err := service.sign(licenseClaimsFromLicense(license))
	if err != nil {
		return "", License{}, err
	}
	return token, license, nil
}

func (service *Service) Verify(token, workspaceID string) (License, error) {
	if len(service.secret) == 0 {
		return License{}, errors.New("license signing secret is not configured")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return License{}, errors.New("invalid license format")
	}
	var header tokenHeader
	if err := decodePart(parts[0], &header); err != nil || header.Algorithm != "HS256" || header.Type != "LICENSE" {
		return License{}, errors.New("invalid license header")
	}
	signature, err := decodeRaw(parts[2])
	if err != nil {
		return License{}, errors.New("invalid license signature")
	}
	mac := hmac.New(sha256.New, service.secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return License{}, errors.New("license signature verification failed")
	}
	var claims licenseClaims
	if err := decodePart(parts[1], &claims); err != nil {
		return License{}, errors.New("invalid license claims")
	}
	license := licenseFromClaims(claims)
	if err := validateLicense(license, time.Now().UTC(), true); err != nil {
		return License{}, err
	}
	if license.WorkspaceID != "" && license.WorkspaceID != workspaceID {
		return License{}, errors.New("license is for a different workspace")
	}
	return license, nil
}

func (service *Service) sign(claims licenseClaims) (string, error) {
	if len(service.secret) == 0 {
		return "", errors.New("license signing secret is not configured")
	}
	header, err := encodePart(tokenHeader{Algorithm: "HS256", Type: "LICENSE"})
	if err != nil {
		return "", err
	}
	payload, err := encodePart(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.secret)
	_, _ = mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func licenseClaimsFromLicense(license License) licenseClaims {
	return licenseClaims{
		ID: license.ID, CustomerName: license.CustomerName, SeatCount: license.SeatCount,
		LicenseType: license.LicenseType, IssuedAt: license.IssuedAt.Unix(), ExpiresAt: license.ExpiresAt.Unix(),
		Trial: license.Trial, Features: license.Features, WorkspaceID: license.WorkspaceID,
	}
}

func licenseFromClaims(claims licenseClaims) License {
	features := normalizeFeatures(claims.Features)
	if len(features) == 0 {
		features = append([]string(nil), AllFeatures...)
	}
	return License{
		ID: claims.ID, CustomerName: claims.CustomerName, SeatCount: claims.SeatCount,
		LicenseType: claims.LicenseType, IssuedAt: time.Unix(claims.IssuedAt, 0).UTC(),
		ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(), Trial: claims.Trial,
		Features: features, WorkspaceID: claims.WorkspaceID,
	}
}

func validateLicense(license License, now time.Time, checkTime bool) error {
	if license.ID == "" || license.CustomerName == "" {
		return errors.New("license id and customer name are required")
	}
	if license.SeatCount < 1 {
		return errors.New("license seat count must be positive")
	}
	if license.LicenseType != LicenseTypeBusiness && license.LicenseType != LicenseTypeEnterprise {
		return fmt.Errorf("unsupported license type %q", license.LicenseType)
	}
	if license.ExpiresAt.IsZero() || !license.ExpiresAt.After(license.IssuedAt) {
		return errors.New("license expiry must be after issue time")
	}
	if checkTime {
		if !license.ExpiresAt.After(now) {
			return errors.New("license has expired")
		}
		if license.IssuedAt.After(now.Add(5 * time.Minute)) {
			return errors.New("license issue time is in the future")
		}
	}
	return nil
}

func normalizeFeatures(features []string) []string {
	seen := make(map[string]struct{}, len(features))
	result := make([]string, 0, len(features))
	for _, feature := range features {
		feature = strings.TrimSpace(feature)
		if feature == "" {
			continue
		}
		if _, ok := seen[feature]; ok {
			continue
		}
		seen[feature] = struct{}{}
		result = append(result, feature)
	}
	return result
}

func encodePart(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodePart(value string, destination any) error {
	data, err := decodeRaw(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}

func decodeRaw(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }

func newID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(buffer[0:4]), hex.EncodeToString(buffer[4:6]), hex.EncodeToString(buffer[6:8]), hex.EncodeToString(buffer[8:10]), hex.EncodeToString(buffer[10:16])), nil
}
