package http

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceSettingEnabled(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		want     bool
	}{
		{name: "enabled", settings: `{"api":{"restrictToAdmins":true}}`, want: true},
		{name: "disabled", settings: `{"api":{"restrictToAdmins":false}}`, want: false},
		{name: "missing", settings: `{"security":{"mfa":true}}`, want: false},
		{name: "invalid", settings: `{`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := workspaceSettingEnabled(json.RawMessage(test.settings), "api", "restrictToAdmins"); got != test.want {
				t.Fatalf("workspaceSettingEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}
