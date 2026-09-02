package jwt

import (
	"testing"
	"time"

	"centipede/internal/modules/identity/domain"
)

func TestServiceRoundTripAndRejectsTampering(t *testing.T) {
	service := New("a secret that is long enough for tests", "centipede")
	token, err := service.IssueAccessToken(domain.User{ID: 42}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if id, err := service.ParseAccessToken(token); err != nil || id != 42 {
		t.Fatalf("round trip failed: %d %v", id, err)
	}
	last := token[len(token)-1]
	replacement := byte('a')
	if last == replacement {
		replacement = 'b'
	}
	tampered := token[:len(token)-1] + string(replacement)
	if _, err := service.ParseAccessToken(tampered); err == nil {
		t.Fatal("expected tampered token rejection")
	}
}
