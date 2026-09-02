package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidEntry = errors.New("invalid vocabulary entry")
	ErrNotFound     = errors.New("vocabulary entry not found")
)

var languagePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)

const (
	StatusNew      = "new"
	StatusLearning = "learning"
	StatusKnown    = "known"
	StatusPaused   = "paused"
)

type Entry struct {
	ID             int64
	UserID         int64
	LanguageTag    string
	OriginalText   string
	NormalizedText string
	Lemma          string
	Definition     string
	Notes          string
	Status         string
	Source         Source
	Contexts       []Context
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Source struct {
	Book     string `json:"book"`
	Chapter  string `json:"chapter"`
	Location string `json:"location"`
}

type Context struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	Location string `json:"location"`
}

func NormalizeLanguageTag(raw string) string {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(raw), "_", "-"), "-")
	if len(parts) == 0 {
		return ""
	}
	parts[0] = strings.ToLower(parts[0])
	for index := 1; index < len(parts); index++ {
		if len(parts[index]) == 4 {
			parts[index] = strings.ToUpper(parts[index][:1]) + strings.ToLower(parts[index][1:])
		} else if len(parts[index]) == 2 || len(parts[index]) == 3 && !isDigits(parts[index]) {
			parts[index] = strings.ToUpper(parts[index])
		} else {
			parts[index] = strings.ToLower(parts[index])
		}
	}
	return strings.Join(parts, "-")
}

func (entry Entry) Validate() error {
	if entry.UserID <= 0 || !languagePattern.MatchString(entry.LanguageTag) || strings.TrimSpace(entry.OriginalText) == "" || len(entry.OriginalText) > 1000 {
		return ErrInvalidEntry
	}
	if entry.Status != StatusNew && entry.Status != StatusLearning && entry.Status != StatusKnown && entry.Status != StatusPaused {
		return ErrInvalidEntry
	}
	for _, context := range entry.Contexts {
		if strings.TrimSpace(context.Text) == "" || len(context.Text) > 5000 {
			return ErrInvalidEntry
		}
	}
	return nil
}

func isDigits(raw string) bool {
	if raw == "" {
		return false
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
