package application

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"centipede/internal/modules/identity/domain"
)

type authUsers struct {
	nextID int64
	users  map[string]domain.User
}

func newAuthUsers() *authUsers { return &authUsers{nextID: 1, users: make(map[string]domain.User)} }

func (users *authUsers) CreateLocalUser(_ context.Context, user domain.User) (domain.User, error) {
	if _, exists := users.users[user.Email]; exists {
		return domain.User{}, errors.New("unique email")
	}
	user.ID = users.nextID
	users.nextID++
	user.CreatedAt = time.Now()
	user.UpdatedAt = user.CreatedAt
	users.users[user.Email] = user
	return user, nil
}
func (users *authUsers) FindLocalByEmail(_ context.Context, email string) (domain.User, error) {
	user, ok := users.users[email]
	if !ok {
		return domain.User{}, errors.New("not found")
	}
	return user, nil
}
func (users *authUsers) FindByID(_ context.Context, id int64) (domain.User, error) {
	for _, user := range users.users {
		if user.ID == id {
			return user, nil
		}
	}
	return domain.User{}, errors.New("not found")
}

type authSessions struct {
	sessions map[string]Session
	revoked  map[string]bool
}

func newAuthSessions() *authSessions {
	return &authSessions{sessions: make(map[string]Session), revoked: make(map[string]bool)}
}
func (sessions *authSessions) CreateSession(_ context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error {
	sessions.sessions[string(tokenHash)] = Session{UserID: userID, ExpiresAt: expiresAt}
	return nil
}
func (sessions *authSessions) FindActiveSession(_ context.Context, tokenHash []byte, now time.Time) (Session, error) {
	session, ok := sessions.sessions[string(tokenHash)]
	if !ok || sessions.revoked[string(tokenHash)] || !session.ExpiresAt.After(now) {
		return Session{}, errors.New("not found")
	}
	return session, nil
}
func (sessions *authSessions) RevokeSession(_ context.Context, tokenHash []byte, _ time.Time) error {
	sessions.revoked[string(tokenHash)] = true
	return nil
}

type authHasher struct{}

func (authHasher) Hash(raw string) (string, error) { return "hash:" + raw, nil }
func (authHasher) Compare(encoded, raw string) error {
	if encoded != "hash:"+raw {
		return errors.New("wrong")
	}
	return nil
}

type authTokens struct{}

func (authTokens) IssueAccessToken(user domain.User, _ time.Time) (string, error) {
	return "access:" + strconv.FormatInt(user.ID, 10), nil
}
func (authTokens) ParseAccessToken(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(raw, "access:"), 10, 64)
}

func newTestAuth() (*AuthService, *authUsers, *authSessions) {
	users := newAuthUsers()
	sessions := newAuthSessions()
	service := NewAuthService(users, sessions, authHasher{}, authTokens{}, time.Minute, time.Hour)
	service.now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	return service, users, sessions
}

func TestRegisterLoginAndRefreshRotateSession(t *testing.T) {
	service, _, sessions := newTestAuth()
	registered, err := service.Register(context.Background(), " Reader@Example.COM ", "correct horse", "Reader")
	if err != nil {
		t.Fatal(err)
	}
	if registered.User.Email != "reader@example.com" || registered.User.PasswordHash != "hash:correct horse" {
		t.Fatalf("unexpected user: %#v", registered.User)
	}
	if registered.AccessToken == "" || registered.RefreshToken == "" {
		t.Fatal("expected tokens")
	}
	loggedIn, err := service.Login(context.Background(), "reader@example.com", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if loggedIn.User.ID != registered.User.ID {
		t.Fatal("login returned a different user")
	}
	refreshed, err := service.Refresh(context.Background(), registered.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RefreshToken == registered.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if _, err := service.Refresh(context.Background(), registered.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("expected old token rejection, got %v", err)
	}
	if len(sessions.revoked) != 1 {
		t.Fatalf("expected one revoked session, got %d", len(sessions.revoked))
	}
}

func TestRegisterRejectsWeakPasswordAndDuplicateEmail(t *testing.T) {
	service, _, _ := newTestAuth()
	if _, err := service.Register(context.Background(), "reader@example.com", "short", ""); err == nil {
		t.Fatal("expected weak password error")
	}
	if _, err := service.Register(context.Background(), "reader@example.com", "correct horse", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), "reader@example.com", "another horse", ""); !errors.Is(err, ErrEmailAlreadyExists) {
		t.Fatalf("expected duplicate email error, got %v", err)
	}
}
