package http

import (
	"testing"
	"time"
)

func TestVerificationConfig(t *testing.T) {
	amount := 2
	mode, gotAmount, unit, expiresAt, ok := verificationConfig(pageVerificationRequest{
		Mode: "week", PeriodAmount: &amount, PeriodUnit: "week",
	}, "expiring")
	if ok || mode != nil || gotAmount != nil || unit != nil || expiresAt != nil {
		t.Fatalf("invalid mode unexpectedly accepted")
	}

	mode, gotAmount, unit, expiresAt, ok = verificationConfig(pageVerificationRequest{
		Mode: "period", PeriodAmount: &amount, PeriodUnit: "week",
	}, "expiring")
	if !ok || mode == nil || *mode != "period" || gotAmount == nil || *gotAmount != amount || unit == nil || *unit != "week" || expiresAt == nil {
		t.Fatalf("valid period configuration was rejected")
	}
	if !expiresAt.After(time.Now().UTC()) {
		t.Fatalf("period expiry should be in the future")
	}

	_, _, _, _, ok = verificationConfig(pageVerificationRequest{}, "qms")
	if !ok {
		t.Fatalf("qms configuration should not require expiration fields")
	}
}
