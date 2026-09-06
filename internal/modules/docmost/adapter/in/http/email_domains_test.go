package http

import "testing"

func TestNormalizeEmailDomains(t *testing.T) {
	got := normalizeEmailDomains([]string{" Example.COM ", "not a domain", "example.com", "sub.example.com/path"})
	want := []string{"example.com", "sub.example.com"}
	if len(got) != len(want) {
		t.Fatalf("unexpected domain count: got %#v want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unexpected domains: got %#v want %#v", got, want)
		}
	}
}

func TestEmailDomainAllowed(t *testing.T) {
	if !emailDomainAllowed("person@example.com", []string{"example.com"}) {
		t.Fatal("expected approved domain")
	}
	if emailDomainAllowed("person@other.com", []string{"example.com"}) {
		t.Fatal("expected unapproved domain")
	}
	if !emailDomainAllowed("person@other.com", nil) {
		t.Fatal("an empty allowlist should not restrict invitations")
	}
}
