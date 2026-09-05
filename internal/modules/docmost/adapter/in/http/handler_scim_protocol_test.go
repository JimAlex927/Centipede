package http

import (
	"reflect"
	"testing"

	"centipede/internal/modules/docmost/adapter/out/postgres"
)

func TestParseSCIMFilter(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		want    string
		wantErr bool
	}{
		{name: "empty", filter: "", want: ""},
		{name: "quoted user name", filter: `userName eq "person@example.com"`, want: "person@example.com"},
		{name: "external id", filter: `externalId eq "id-42"`, want: "id-42"},
		{name: "unsupported operator", filter: `userName co "person"`, wantErr: true},
		{name: "unsupported attribute", filter: `email eq "person@example.com"`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSCIMFilter(test.filter, "userName", "externalId")
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("parseSCIMFilter(%q) = %q, %v; want %q, error=%v", test.filter, got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestApplySCIMUserPatch(t *testing.T) {
	displayName := "Old Name"
	active := true
	input := postgres.SCIMUserInput{UserName: "old@example.com", DisplayName: &displayName, Active: &active}
	patch := []scimPatchOperation{
		{Path: "userName", Value: "new@example.com"},
		{Path: "displayName", Value: "New Name"},
		{Path: "active", Value: false},
	}
	for _, operation := range patch {
		if err := applySCIMUserPatch(&input, operation); err != nil {
			t.Fatal(err)
		}
	}
	if input.UserName != "new@example.com" || input.DisplayName == nil || *input.DisplayName != "New Name" || input.Active == nil || *input.Active {
		t.Fatalf("unexpected SCIM patch result: %#v", input)
	}
	if err := applySCIMUserPatch(&input, scimPatchOperation{Value: map[string]any{"active": true, "displayName": "Nested"}}); err != nil {
		t.Fatal(err)
	}
	if input.Active == nil || !*input.Active || input.DisplayName == nil || *input.DisplayName != "Nested" {
		t.Fatalf("nested SCIM patch was not applied: %#v", input)
	}
	if err := applySCIMUserPatch(&input, scimPatchOperation{Path: "password", Value: "secret"}); err == nil {
		t.Fatal("unsupported SCIM patch path was accepted")
	}
}

func TestSCIMMemberValues(t *testing.T) {
	got := scimMemberValues([]scimMemberBody{{Value: "u1"}, {Value: " "}, {Value: "u2"}})
	if !reflect.DeepEqual(got, []string{"u1", "u2"}) {
		t.Fatalf("unexpected SCIM member values: %#v", got)
	}
}
