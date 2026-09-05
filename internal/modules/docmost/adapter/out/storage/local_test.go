package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"centipede/internal/modules/docmost/application"
)

func TestOversizedUploadPreservesExistingFile(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = store.Save(ctx, "files/test.txt", strings.NewReader("original"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, "files/test.txt", strings.NewReader("too large"), 2); !errors.Is(err, application.ErrUploadTooLarge) {
		t.Fatalf("expected size error, got %v", err)
	}
	file, err := store.Open(ctx, "files/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "original" {
		t.Fatalf("original file changed: %q, %v", data, err)
	}
}

func TestStorageRejectsEscapingAndWindowsStreamPaths(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../secret", "/absolute", "C:/absolute", "files/test.txt:stream", `..\secret`, ".", ""} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Save(context.Background(), name, strings.NewReader("data"), 10); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestCancelledUploadDoesNotCreateFile(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.Save(ctx, "files/cancelled.txt", strings.NewReader("data"), 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if file, err := store.Open(context.Background(), "files/cancelled.txt"); err == nil {
		file.Close()
		t.Fatal("cancelled upload created a file")
	}
}
