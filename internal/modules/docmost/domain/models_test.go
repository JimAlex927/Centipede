package domain

import (
	"encoding/json"
	"testing"
)

func TestAPIModelsKeepMeaningfulZeroValues(t *testing.T) {
	share, err := json.Marshal(Share{Level: 0})
	if err != nil {
		t.Fatalf("marshal share: %v", err)
	}
	if string(share) == "{}" || !jsonFieldExists(t, share, "level") {
		t.Fatalf("share level=0 was omitted: %s", share)
	}

	workspace, err := json.Marshal(Workspace{MemberCount: 0})
	if err != nil {
		t.Fatalf("marshal workspace: %v", err)
	}
	if !jsonFieldExists(t, workspace, "memberCount") {
		t.Fatalf("workspace memberCount=0 was omitted: %s", workspace)
	}

	member, err := json.Marshal(PagePermissionMember{})
	if err != nil {
		t.Fatalf("marshal permission member: %v", err)
	}
	for _, field := range []string{"memberCount", "isDefault"} {
		if !jsonFieldExists(t, member, field) {
			t.Fatalf("permission member %s was omitted: %s", field, member)
		}
	}
}

func jsonFieldExists(t *testing.T, payload []byte, field string) bool {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	_, exists := value[field]
	return exists
}
