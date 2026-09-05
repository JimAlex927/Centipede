package postgres

import (
	"encoding/json"
	"testing"
)

func TestBaseFilterMatchesNestedConditions(t *testing.T) {
	filterJSON := json.RawMessage(`{"op":"and","children":[{"propertyId":"title","op":"contains","value":"go"},{"propertyId":"count","op":"gte","value":2}]}`)
	filter, err := decodeBaseFilter(filterJSON)
	if err != nil {
		t.Fatal(err)
	}
	row := BaseRow{Cells: json.RawMessage(`{"title":"Go migration","count":3}`)}
	if !baseFilterMatches(filter, row) {
		t.Fatal("expected row to match filter")
	}
	row.Cells = json.RawMessage(`{"title":"Node migration","count":3}`)
	if baseFilterMatches(filter, row) {
		t.Fatal("expected row not to match filter")
	}
}

func TestBaseFilterDateExactObject(t *testing.T) {
	filterJSON := json.RawMessage(`{"propertyId":"due","op":"eq","value":{"mode":"exact","date":"2026-09-05"}}`)
	filter, err := decodeBaseFilter(filterJSON)
	if err != nil {
		t.Fatal(err)
	}
	row := BaseRow{Cells: json.RawMessage(`{"due":"2026-09-05T10:00:00Z"}`)}
	if !baseFilterMatches(filter, row) {
		t.Fatal("expected exact date equality filter to match")
	}
}

func TestBaseFilterEmptyAndMultiValueOperators(t *testing.T) {
	if !baseValueMatches([]any{"a", "b"}, "any", []any{"b", "c"}) {
		t.Fatal("expected any operator to match")
	}
	if !baseValueMatches([]any{"a", "b"}, "all", []any{"a", "b"}) {
		t.Fatal("expected all operator to match")
	}
	if !baseValueMatches("", "isEmpty", nil) || !baseValueMatches("x", "isNotEmpty", nil) {
		t.Fatal("empty operators did not match")
	}
}
