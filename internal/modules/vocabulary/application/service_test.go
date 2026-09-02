package application

import (
	"context"
	"testing"

	"centipede/internal/modules/vocabulary/domain"
)

type fakeVocabularyRepository struct{ created domain.Entry }

func (fake *fakeVocabularyRepository) Create(_ context.Context, entry domain.Entry) (domain.Entry, error) {
	fake.created = entry
	entry.ID = 1
	return entry, nil
}
func (*fakeVocabularyRepository) List(context.Context, Filter) ([]domain.Entry, error) {
	return nil, nil
}
func (*fakeVocabularyRepository) FindByID(context.Context, int64, int64) (domain.Entry, error) {
	return domain.Entry{}, nil
}
func (*fakeVocabularyRepository) UpdateStatus(context.Context, int64, int64, string) (domain.Entry, error) {
	return domain.Entry{}, nil
}

func TestCreateNormalizesMultilingualEntry(t *testing.T) {
	repository := &fakeVocabularyRepository{}
	service := NewService(repository)
	entry, err := service.Create(context.Background(), domain.Entry{UserID: 7, LanguageTag: " EN_us ", OriginalText: "  Hello  ", Source: domain.Source{Book: "  A book "}, Contexts: []domain.Context{{Text: "  Hello there. "}}})
	if err != nil {
		t.Fatal(err)
	}
	if entry.LanguageTag != "en-US" || entry.OriginalText != "Hello" || entry.NormalizedText != "hello" || entry.Status != domain.StatusNew {
		t.Fatalf("unexpected normalized entry: %#v", entry)
	}
	if repository.created.Source.Book != "A book" || repository.created.Contexts[0].Text != "Hello there." {
		t.Fatalf("source/context not normalized: %#v", repository.created)
	}
}
