package http

import (
	"encoding/base64"
	"testing"

	"github.com/crewjam/saml"
)

func TestSAMLIdentityUsesNameIDAndCommonAttributes(t *testing.T) {
	assertion := &saml.Assertion{
		Subject: &saml.Subject{NameID: &saml.NameID{Value: "idp-user-1"}},
		AttributeStatements: []saml.AttributeStatement{{Attributes: []saml.Attribute{
			{Name: "mail", Values: []saml.AttributeValue{{Value: "User@example.com"}}},
			{FriendlyName: "displayName", Values: []saml.AttributeValue{{Value: "Example User"}}},
		}}},
	}
	providerID, email, name := samlIdentity(assertion)
	if providerID != "idp-user-1" || email != "User@example.com" || name != "Example User" {
		t.Fatalf("unexpected SAML identity: %q %q %q", providerID, email, name)
	}
}

func TestSAMLIdentityFallsBackToEmailForProviderID(t *testing.T) {
	assertion := &saml.Assertion{
		AttributeStatements: []saml.AttributeStatement{
			{Attributes: []saml.Attribute{
				{Name: "email", Values: []saml.AttributeValue{{Value: "user@example.com"}}},
			}},
		},
	}
	providerID, email, _ := samlIdentity(assertion)
	if providerID != email || providerID != "user@example.com" {
		t.Fatalf("expected email fallback, got %q and %q", providerID, email)
	}
}

func TestLDAPSearchFilterEscapesUsername(t *testing.T) {
	configured := "(&(objectClass=person)(uid={{username}}))"
	filter := ldapSearchFilter(&configured, "a*)(uid=*)")
	want := "(&(objectClass=person)(uid=a\\2a\\29\\28uid=\\2a\\29))"
	if filter != want {
		t.Fatalf("unexpected LDAP filter: %q", filter)
	}
}

func TestParseSAMLSigningCertificateRejectsInvalidDER(t *testing.T) {
	der := []byte{0x30, 0x03, 0x01, 0x01, 0x00}
	_, _, err := parseSAMLSigningCertificate(base64.StdEncoding.EncodeToString(der))
	if err == nil {
		t.Fatal("expected invalid certificate error")
	}
}
