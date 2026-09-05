package http

import (
	"strings"
	"testing"
	"time"
)

func TestTOTPUsesRFC6238Algorithm(t *testing.T) {
	// RFC 6238 test secret: 12345678901234567890, encoded without padding.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got := totpCode(secret, 59/30); got != "287082" {
		t.Fatalf("totp code = %s, want 287082", got)
	}
	if !verifyTOTP(secret, "287082", time.Unix(59, 0).UTC()) {
		t.Fatal("expected RFC test code to verify")
	}
	if verifyTOTP(secret, "000000", time.Unix(59, 0).UTC()) {
		t.Fatal("unexpectedly accepted invalid code")
	}
}

func TestBackupCodeGeneration(t *testing.T) {
	plain, stored, err := generateBackupCodes(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 8 || len(stored) != 8 {
		t.Fatalf("generated %d plain and %d stored codes", len(plain), len(stored))
	}
	for i, code := range plain {
		if len(code) != 8 || strings.ContainsAny(code, "01IO") {
			t.Fatalf("backup code %q has an invalid format", code)
		}
		if stored[i] != hashBackupCode(code) {
			t.Fatalf("backup code %q was not hashed consistently", code)
		}
	}
}
