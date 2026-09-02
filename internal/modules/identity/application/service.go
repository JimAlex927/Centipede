package application

import (
	"context"
	"strings"

	"centipede/internal/modules/identity/domain"
)

type UserRepository interface {
	UpsertExternalIdentity(context.Context, domain.ExternalIdentity) (domain.User, error)
	FindByID(context.Context, int64) (domain.User, error)
}

type Service struct {
	users UserRepository
}

func NewService(users UserRepository) *Service {
	return &Service{users: users}
}

func (service *Service) EnsureUser(ctx context.Context, identity domain.ExternalIdentity) (domain.User, error) {
	identity.Issuer = strings.TrimSpace(identity.Issuer)
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.Email = strings.ToLower(strings.TrimSpace(identity.Email))
	if err := identity.Validate(); err != nil {
		return domain.User{}, err
	}
	return service.users.UpsertExternalIdentity(ctx, identity)
}

func (service *Service) User(ctx context.Context, id int64) (domain.User, error) {
	return service.users.FindByID(ctx, id)
}
