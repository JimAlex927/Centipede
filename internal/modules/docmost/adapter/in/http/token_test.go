package http

import (
	"strings"
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	service := newTokenService("test-secret-not-for-production")
	for _, kind := range []string{"access", "collab"} {
		t.Run(kind, func(t *testing.T) {
			claims := tokenClaims{Subject: "user", WorkspaceID: "workspace", Type: kind}
			if kind == "access" {
				claims.SessionID = "session"
			}
			raw, err := service.issue(claims, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.parse(raw, kind)
			if err != nil {
				t.Fatal(err)
			}
			if got.Subject != claims.Subject || got.WorkspaceID != claims.WorkspaceID || got.SessionID != claims.SessionID {
				t.Fatalf("claims changed: %+v", got)
			}
		})
	}
}

func TestAttachmentTokenAllowsPageScopedClaims(t *testing.T) {
	service := newTokenService("test-secret-not-for-production")
	raw, err := service.issue(tokenClaims{
		AttachmentID: "attachment",
		PageID:       "page",
		WorkspaceID:  "workspace",
		Type:         "attachment",
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.parse(raw, "attachment")
	if err != nil || claims.AttachmentID != "attachment" || claims.PageID != "page" {
		t.Fatalf("attachment token was not parsed: %+v, %v", claims, err)
	}
}

func TestTokenRejectsInvalidClaims(t *testing.T) {
	service := newTokenService("test-secret-not-for-production")
	now := time.Now().Unix()
	for _, name := range []string{"expired", "future", "user", "workspace", "session", "purpose", "algorithm", "header-type", "signature", "malformed"} {
		t.Run(name, func(t *testing.T) {
			claims := tokenClaims{Subject: "user", WorkspaceID: "workspace", SessionID: "session", Type: "access", IssuedAt: now, ExpiresAt: now + 3600}
			header := map[string]string{"alg": "HS256", "typ": "JWT"}
			switch name {
			case "expired":
				claims.ExpiresAt = now - 1
			case "future":
				claims.IssuedAt = now + 3600
			case "user":
				claims.Subject = ""
			case "workspace":
				claims.WorkspaceID = ""
			case "session":
				claims.SessionID = ""
			case "purpose":
				claims.Type = "collab"
			case "algorithm":
				header["alg"] = "none"
			case "header-type":
				header["typ"] = "other"
			}
			h, err := encodeTokenPart(header)
			if err != nil {
				t.Fatal(err)
			}
			p, err := encodeTokenPart(claims)
			if err != nil {
				t.Fatal(err)
			}
			unsigned := h + "." + p
			raw := unsigned + "." + service.sign(unsigned)
			if name == "signature" {
				raw = unsigned + "." + newTokenService("different-secret").sign(unsigned)
			}
			if name == "malformed" {
				raw = strings.TrimSuffix(raw, "."+service.sign(unsigned))
			}
			if _, err := service.parse(raw, "access"); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}
