package http

import "testing"

func TestSpacePermissionsMatchDocmostRoles(t *testing.T) {
	tests := []struct {
		role   string
		manage bool
	}{
		{role: "admin", manage: true},
		{role: "writer", manage: true},
		{role: "reader", manage: false},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			permissions := spacePermissions(test.role)
			if len(permissions) != 4 {
				t.Fatalf("got %d permissions, want 4", len(permissions))
			}
			for _, permission := range permissions {
				if permission.Subject != "page" {
					continue
				}
				if (permission.Action == "manage") != test.manage {
					t.Fatalf("page permission is %+v", permission)
				}
				return
			}
			t.Fatal("page permission missing")
		})
	}
}
