package http

import "testing"

func TestIndexableAttachmentExtensions(t *testing.T) {
	for _, extension := range []string{".pdf", ".DOCX", ".md", ".txt", ".drawio"} {
		if !isIndexableAttachmentExtension(extension) {
			t.Fatalf("indexable attachment extension rejected: %q", extension)
		}
	}
	for _, extension := range []string{".png", ".zip", ".mp4"} {
		if isIndexableAttachmentExtension(extension) {
			t.Fatalf("non-text attachment unexpectedly indexable: %q", extension)
		}
	}
}
