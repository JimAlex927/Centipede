package http

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestOAuthRedirectPreservesQueryAndState(t *testing.T) {
	got := oauthRedirectCode("https://client.example/callback?existing=1", "state value", "abc")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("existing") != "1" || query.Get("code") != "abc" || query.Get("state") != "state value" {
		t.Fatalf("unexpected redirect query: %s", parsed.RawQuery)
	}
}

func TestValidOAuthRedirectURI(t *testing.T) {
	if !validOAuthRedirectURI("http://localhost:4318/callback") {
		t.Fatal("expected localhost redirect URI to be valid")
	}
	if validOAuthRedirectURI("https://client.example/callback#fragment") {
		t.Fatal("redirect URI fragments must be rejected")
	}
}

func TestOAuthApprovalRequestDecodesEmbeddedParameters(t *testing.T) {
	var request oauthApprovalRequest
	if err := json.Unmarshal([]byte(`{"client_id":"client","redirect_uri":"https://client.example/callback","approved":true,"approvedScopes":["read"]}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.ClientID != "client" || request.RedirectURI == "" || !request.Approved || len(request.ApprovedScopes) != 1 {
		t.Fatalf("approval request did not decode: %+v", request)
	}
}
