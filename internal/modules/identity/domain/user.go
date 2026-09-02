package domain

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidIdentity = errors.New("invalid external identity")

type ExternalIdentity struct {
	Issuer  string
	Subject string
	Email   string
}

type User struct {
	ID              int64
	IdentityIssuer  string
	IdentitySubject string
	Email           string
	DisplayName     string
	PasswordHash    string
	Status          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (user User) IsActive() bool {
	return user.Status == "" || user.Status == "active"
}

func (identity ExternalIdentity) Validate() error {
	if strings.TrimSpace(identity.Issuer) == "" || strings.TrimSpace(identity.Subject) == "" {
		return ErrInvalidIdentity
	}
	return nil
}
