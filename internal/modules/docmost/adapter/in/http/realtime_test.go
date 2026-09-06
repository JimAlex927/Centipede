package http

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRealtimeCheckOriginAllowsSameOriginAndConfiguredOrigins(t *testing.T) {
	handler := &RealtimeHandler{allowedOrigins: map[string]struct{}{
		"http://frontend.example:5173": {},
	}}

	tests := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{name: "same origin with port", origin: "http://docs.example:8080", host: "docs.example:8080", want: true},
		{name: "same origin without port", origin: "https://docs.example", host: "docs.example", want: true},
		{name: "configured cross origin", origin: "http://frontend.example:5173", host: "api.example:7788", want: true},
		{name: "unknown origin", origin: "http://attacker.example", host: "docs.example:8080", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://"+tt.host+"/realtime", nil)
			request.Host = tt.host
			request.Header.Set("Origin", tt.origin)
			if got := handler.checkOrigin(request); got != tt.want {
				t.Fatalf("checkOrigin(%q, host %q) = %v; want %v", tt.origin, tt.host, got, tt.want)
			}
		})
	}
}

func TestRealtimeEventAuthorizationScope(t *testing.T) {
	tests := []struct {
		name, data, page string
		inbound, valid   bool
	}{
		{"created page", `{"operation":"addTreeNode","payload":{"data":{"id":"page"}}}`, "page", true, true},
		{"moved page", `{"operation":"moveTreeNode","payload":{"id":"page"}}`, "page", true, true},
		{"deleted page", `{"operation":"deleteTreeNode","payload":{"node":{"id":"page"}}}`, "page", true, true},
		{"title update", `{"operation":"updateOne","id":"page"}`, "page", true, true},
		{"missing page", `{"operation":"updateOne"}`, "", true, false},
		{"forged comment", `{"operation":"commentCreated","pageId":"page"}`, "", true, false},
		{"server comment", `{"operation":"commentCreated","pageId":"page"}`, "page", false, true},
		{"arbitrary event", `{"operation":"invalidate","entity":["users"]}`, "", true, false},
		{"refetch", `{"operation":"refetchRootTreeNodeEvent"}`, "", true, true},
		{"invalid json", `{`, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, valid := realtimePageID(json.RawMessage(tt.data), tt.inbound)
			if page != tt.page || valid != tt.valid {
				t.Fatalf("got %q/%v; want %q/%v", page, valid, tt.page, tt.valid)
			}
		})
	}
}
