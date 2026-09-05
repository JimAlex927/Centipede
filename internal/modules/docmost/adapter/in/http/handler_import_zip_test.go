package http

import "testing"

func TestAddZipParentDirectories(t *testing.T) {
	directories := map[string]bool{}
	addZipParentDirectories(directories, "docs/team")
	if !directories["docs/team"] || !directories["docs"] || len(directories) != 2 {
		t.Fatalf("unexpected directories: %#v", directories)
	}
}

func TestImportTaskUUID(t *testing.T) {
	value, err := importTaskUUID()
	if err != nil || len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		t.Fatalf("invalid task uuid %q: %v", value, err)
	}
}
