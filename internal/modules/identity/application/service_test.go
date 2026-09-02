package application

import (
	"context"
	"testing"

	"centipede/internal/modules/identity/domain"
)

type fakeUsers struct {
	identity domain.ExternalIdentity
}

func (fake *fakeUsers) UpsertExternalIdentity(_ context.Context, identity domain.ExternalIdentity) (domain.User, error) {
	fake.identity = identity
	return domain.User{ID: 1, IdentityIssuer: identity.Issuer, IdentitySubject: identity.Subject, Email: identity.Email}, nil
}

func (*fakeUsers) FindByID(context.Context, int64) (domain.User, error) {
	return domain.User{ID: 1}, nil
}

func TestEnsureUserNormalizesIdentity(t *testing.T) {
	repository := &fakeUsers{}
	service := NewService(repository)

	user, err := service.EnsureUser(context.Background(), domain.ExternalIdentity{
		Issuer: " allmacht-auth-center ", Subject: " 42 ", Email: " User@Example.COM ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "user@example.com" {
		t.Fatalf("unexpected normalized email %q", user.Email)
	}
	if repository.identity.Issuer != "allmacht-auth-center" || repository.identity.Subject != "42" {
		t.Fatalf("identity was not normalized: %#v", repository.identity)
	}
}

func TestEnsureUserRejectsMissingSubject(t *testing.T) {
	service := NewService(&fakeUsers{})
	if _, err := service.EnsureUser(context.Background(), domain.ExternalIdentity{Issuer: "allmacht"}); err == nil {
		t.Fatal("expected invalid identity error")
	}
}
