package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"centipede/internal/modules/identity/domain"
)

var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrEmailAlreadyExists  = errors.New("email already exists")
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	ErrInactiveUser        = errors.New("user is inactive")
)

type LocalUserRepository interface {
	CreateLocalUser(context.Context, domain.User) (domain.User, error)
	FindLocalByEmail(context.Context, string) (domain.User, error)
	FindByID(context.Context, int64) (domain.User, error)
}

type Session struct {
	ID        int64
	UserID    int64
	ExpiresAt time.Time
}

type SessionRepository interface {
	CreateSession(context.Context, int64, []byte, time.Time) error
	FindActiveSession(context.Context, []byte, time.Time) (Session, error)
	RevokeSession(context.Context, []byte, time.Time) error
}

type PasswordHasher interface {
	Hash(string) (string, error)
	Compare(string, string) error
}

type TokenIssuer interface {
	IssueAccessToken(domain.User, time.Time) (string, error)
	ParseAccessToken(string) (int64, error)
}

type AuthService struct {
	users      LocalUserRepository
	sessions   SessionRepository
	hasher     PasswordHasher
	tokens     TokenIssuer
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

type AuthResult struct {
	User         domain.User
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func NewAuthService(users LocalUserRepository, sessions SessionRepository, hasher PasswordHasher, tokens TokenIssuer, accessTTL, refreshTTL time.Duration) *AuthService {
	return &AuthService{users: users, sessions: sessions, hasher: hasher, tokens: tokens, accessTTL: accessTTL, refreshTTL: refreshTTL, now: time.Now}
}

func (service *AuthService) Register(ctx context.Context, email, password, displayName string) (AuthResult, error) {
	email, displayName, err := validateRegistration(email, password, displayName)
	if err != nil {
		return AuthResult{}, err
	}
	passwordHash, err := service.hasher.Hash(password)
	if err != nil {
		return AuthResult{}, err
	}
	subject, err := randomID()
	if err != nil {
		return AuthResult{}, err
	}
	user, err := service.users.CreateLocalUser(ctx, domain.User{
		IdentityIssuer: "local", IdentitySubject: subject, Email: email,
		DisplayName: displayName, PasswordHash: passwordHash, Status: "active",
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return AuthResult{}, ErrEmailAlreadyExists
		}
		return AuthResult{}, err
	}
	return service.authenticate(ctx, user)
}

func (service *AuthService) Login(ctx context.Context, email, password string) (AuthResult, error) {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return AuthResult{}, ErrInvalidCredentials
	}
	user, err := service.users.FindLocalByEmail(ctx, email)
	if err != nil || !user.IsActive() || service.hasher.Compare(user.PasswordHash, password) != nil {
		return AuthResult{}, ErrInvalidCredentials
	}
	return service.authenticate(ctx, user)
}

func (service *AuthService) Refresh(ctx context.Context, rawToken string) (AuthResult, error) {
	if strings.TrimSpace(rawToken) == "" {
		return AuthResult{}, ErrInvalidRefreshToken
	}
	now := service.now().UTC()
	oldHash := hashToken(rawToken)
	session, err := service.sessions.FindActiveSession(ctx, oldHash, now)
	if err != nil {
		return AuthResult{}, ErrInvalidRefreshToken
	}
	user, err := service.users.FindByID(ctx, session.UserID)
	if err != nil || !user.IsActive() {
		return AuthResult{}, ErrInvalidRefreshToken
	}
	if err := service.sessions.RevokeSession(ctx, oldHash, now); err != nil {
		return AuthResult{}, err
	}
	return service.authenticate(ctx, user)
}

func (service *AuthService) Logout(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	return service.sessions.RevokeSession(ctx, hashToken(rawToken), service.now().UTC())
}

func (service *AuthService) CurrentUser(ctx context.Context, accessToken string) (domain.User, error) {
	userID, err := service.tokens.ParseAccessToken(accessToken)
	if err != nil {
		return domain.User{}, ErrInvalidCredentials
	}
	user, err := service.users.FindByID(ctx, userID)
	if err != nil || !user.IsActive() {
		return domain.User{}, ErrInvalidCredentials
	}
	return user, nil
}

func (service *AuthService) authenticate(ctx context.Context, user domain.User) (AuthResult, error) {
	now := service.now().UTC()
	refreshToken, err := randomID()
	if err != nil {
		return AuthResult{}, err
	}
	if err := service.sessions.CreateSession(ctx, user.ID, hashToken(refreshToken), now.Add(service.refreshTTL)); err != nil {
		return AuthResult{}, err
	}
	accessToken, err := service.tokens.IssueAccessToken(user, now.Add(service.accessTTL))
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: user, AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: now.Add(service.accessTTL)}, nil
}

func validateRegistration(email, password, displayName string) (string, string, error) {
	email = normalizeEmail(email)
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n") || len(email) > 320 {
		return "", "", errors.New("email is invalid")
	}
	if utf8.RuneCountInString(password) < 8 || utf8.RuneCountInString(password) > 128 {
		return "", "", errors.New("password must contain 8 to 128 characters")
	}
	displayName = strings.TrimSpace(displayName)
	if utf8.RuneCountInString(displayName) > 80 {
		return "", "", errors.New("display name is too long")
	}
	return email, displayName, nil
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func hashToken(raw string) []byte {
	digest := sha256.Sum256([]byte(raw))
	return digest[:]
}

func randomID() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
