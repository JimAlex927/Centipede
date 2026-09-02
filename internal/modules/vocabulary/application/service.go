package application

import (
	"context"
	"strings"

	"centipede/internal/modules/vocabulary/domain"
)

type Repository interface {
	Create(context.Context, domain.Entry) (domain.Entry, error)
	List(context.Context, Filter) ([]domain.Entry, error)
	FindByID(context.Context, int64, int64) (domain.Entry, error)
	UpdateStatus(context.Context, int64, int64, string) (domain.Entry, error)
}

type Filter struct {
	UserID   int64
	Language string
	Status   string
	Query    string
	Limit    int
	Offset   int
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (service *Service) Create(ctx context.Context, entry domain.Entry) (domain.Entry, error) {
	entry.LanguageTag = domain.NormalizeLanguageTag(entry.LanguageTag)
	entry.OriginalText = strings.TrimSpace(entry.OriginalText)
	entry.NormalizedText = strings.ToLower(entry.OriginalText)
	entry.Lemma = strings.TrimSpace(entry.Lemma)
	entry.Definition = strings.TrimSpace(entry.Definition)
	entry.Notes = strings.TrimSpace(entry.Notes)
	entry.Status = strings.TrimSpace(entry.Status)
	if entry.Status == "" {
		entry.Status = domain.StatusNew
	}
	entry.Source.Book = strings.TrimSpace(entry.Source.Book)
	entry.Source.Chapter = strings.TrimSpace(entry.Source.Chapter)
	entry.Source.Location = strings.TrimSpace(entry.Source.Location)
	for index := range entry.Contexts {
		entry.Contexts[index].Text = strings.TrimSpace(entry.Contexts[index].Text)
		entry.Contexts[index].Location = strings.TrimSpace(entry.Contexts[index].Location)
	}
	if err := entry.Validate(); err != nil {
		return domain.Entry{}, err
	}
	return service.repository.Create(ctx, entry)
}

func (service *Service) List(ctx context.Context, filter Filter) ([]domain.Entry, error) {
	filter.Language = domain.NormalizeLanguageTag(filter.Language)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Query = strings.TrimSpace(filter.Query)
	if filter.Status != "" && filter.Status != domain.StatusNew && filter.Status != domain.StatusLearning && filter.Status != domain.StatusKnown && filter.Status != domain.StatusPaused {
		return nil, domain.ErrInvalidEntry
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return service.repository.List(ctx, filter)
}

func (service *Service) FindByID(ctx context.Context, userID, entryID int64) (domain.Entry, error) {
	return service.repository.FindByID(ctx, userID, entryID)
}

func (service *Service) UpdateStatus(ctx context.Context, userID, entryID int64, status string) (domain.Entry, error) {
	status = strings.TrimSpace(status)
	if status != domain.StatusNew && status != domain.StatusLearning && status != domain.StatusKnown && status != domain.StatusPaused {
		return domain.Entry{}, domain.ErrInvalidEntry
	}
	return service.repository.UpdateStatus(ctx, userID, entryID, status)
}
