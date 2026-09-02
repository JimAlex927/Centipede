package domain

import "testing"

func TestNormalizeLanguageTag(t *testing.T) {
	if got := NormalizeLanguageTag(" zh_hans "); got != "zh-Hans" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeLanguageTag("EN-us"); got != "en-US" {
		t.Fatalf("got %q", got)
	}
}

func TestEntryValidation(t *testing.T) {
	entry := Entry{UserID: 3, LanguageTag: "de", OriginalText: "sonderbar", Status: StatusNew, Contexts: []Context{{Text: "Das ist sonderbar."}}}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	entry.Status = "finished"
	if err := entry.Validate(); err == nil {
		t.Fatal("expected invalid status")
	}
}
