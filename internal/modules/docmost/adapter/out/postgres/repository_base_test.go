package postgres

import (
	"encoding/json"
	"testing"
	"time"
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

func TestBaseSystemPropertiesCanBeFiltered(t *testing.T) {
	creator := "11111111-1111-1111-1111-111111111111"
	updatedBy := "22222222-2222-2222-2222-222222222222"
	row := BaseRow{
		Cells:           json.RawMessage(`{}`),
		CreatorID:       &creator,
		LastUpdatedByID: &updatedBy,
		CreatedAt:       time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
	}
	checks := []struct {
		property string
		op       string
		value    string
	}{
		{"createdAt", "onOrAfter", "2026-09-01"},
		{"lastEditedAt", "after", "2026-09-04"},
		{"lastEditedBy", "eq", updatedBy},
	}
	for _, check := range checks {
		filter := &baseFilterNode{PropertyID: check.property, Op: check.op, Value: check.value}
		if !baseFilterMatches(filter, row) {
			t.Errorf("expected %s %s filter to match", check.property, check.op)
		}
	}
}

func TestBaseDateAnchorsAndRanges(t *testing.T) {
	now := time.Date(2026, 9, 5, 15, 0, 0, 0, time.FixedZone("test", 8*60*60))
	oneWeekAgo := baseDateAnchor("oneWeekAgo", now)
	if got := oneWeekAgo.Format("2006-01-02"); got != "2026-08-29" {
		t.Fatalf("oneWeekAgo = %s", got)
	}
	start, end := baseDateRange("thisMonth", now)
	if start.Format("2006-01-02") != "2026-09-01" || end.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("thisMonth range = %s..%s", start, end)
	}
}

func TestBaseSystemPropertySort(t *testing.T) {
	earlier := BaseRow{ID: "a", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	later := BaseRow{ID: "b", CreatedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}
	if compareBaseRows(earlier, later, []baseSort{{PropertyID: "createdAt", Direction: "asc"}}) >= 0 {
		t.Fatal("expected createdAt ascending sort")
	}
}
