package enterprise

import (
	"strings"
	"testing"
	"time"
)

func TestLicenseRoundTrip(t *testing.T) {
	service := NewLicenseService("test-secret")
	token, expected, err := service.Generate(GenerateInput{
		CustomerName: "Acme", SeatCount: 12, LicenseType: LicenseTypeEnterprise,
		IssuedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Features: []string{"ai", "ai", "mcp"}, WorkspaceID: "workspace-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := service.Verify(token, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if actual.ID != expected.ID || actual.CustomerName != "Acme" || actual.SeatCount != 12 || len(actual.Features) != 2 {
		t.Fatalf("unexpected license: %#v", actual)
	}
	if _, err := service.Verify(token, "workspace-2"); err == nil {
		t.Fatal("expected workspace mismatch")
	}
	if _, err := NewLicenseService("wrong-secret").Verify(token, "workspace-1"); err == nil {
		t.Fatal("expected signature failure")
	}
}

func TestLicenseDefaultsToAllFeatures(t *testing.T) {
	service := NewLicenseService("test-secret")
	token, _, err := service.Generate(GenerateInput{CustomerName: "Acme", SeatCount: 1, LicenseType: LicenseTypeBusiness, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	license, err := service.Verify(token, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(license.Features) != len(AllFeatures) || !strings.Contains(strings.Join(license.Features, ","), "audit:logs") {
		t.Fatalf("expected all features, got %#v", license.Features)
	}
}

func TestExpiredLicense(t *testing.T) {
	service := NewLicenseService("test-secret")
	token, _, err := service.Generate(GenerateInput{CustomerName: "Acme", SeatCount: 1, LicenseType: LicenseTypeBusiness, IssuedAt: time.Now().UTC().Add(-2 * time.Hour), ExpiresAt: time.Now().UTC().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(token, ""); err == nil {
		t.Fatal("expected expired license")
	}
}
