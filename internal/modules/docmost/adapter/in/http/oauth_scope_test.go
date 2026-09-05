package http

import "testing"

func TestRequiredOAuthScopeMatchesUpstreamAnnotations(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{"POST", "/api/users/me", "read"},
		{"POST", "/api/pages/create", "write"},
		{"GET", "/api/files/:fileId/:fileName", "read"},
		{"POST", "/api/pages/delete", ""},
	}
	for _, test := range tests {
		if got := requiredOAuthScope(test.method, test.path); got != test.want {
			t.Fatalf("requiredOAuthScope(%q, %q) = %q, want %q", test.method, test.path, got, test.want)
		}
	}
}

func TestHasOAuthScope(t *testing.T) {
	if !hasOAuthScope([]string{"read", "write"}, "read") {
		t.Fatal("read scope should be accepted")
	}
	if hasOAuthScope([]string{"read"}, "write") {
		t.Fatal("write scope should be rejected")
	}
	if !hasOAuthScope(nil, "") {
		t.Fatal("unscoped route should be accepted")
	}
}
