package http

import (
	"encoding/json"
	"testing"
)

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
