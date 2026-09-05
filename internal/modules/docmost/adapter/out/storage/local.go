package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"centipede/internal/modules/docmost/application"
)

type Local struct {
	root string
}

func NewLocal(root string) (*Local, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0o750); err != nil {
		return nil, err
	}
	return &Local{root: filepath.Clean(absolute)}, nil
}

func (storage *Local) Save(ctx context.Context, relativePath string, source io.Reader, maxBytes int64) (int64, error) {
	target, err := storage.resolve(relativePath)
	if err != nil {
		return 0, err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return 0, err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}()
	written, err := io.Copy(temporary, io.LimitReader(&contextReader{ctx: ctx, reader: source}, maxBytes+1))
	if err != nil {
		return 0, err
	}
	if written > maxBytes {
		return 0, application.ErrUploadTooLarge
	}
	if err = temporary.Sync(); err != nil {
		return 0, err
	}
	if err = temporary.Close(); err != nil {
		return 0, err
	}
	if err = os.Rename(temporaryName, target); err != nil {
		return 0, err
	}
	return written, nil
}

func (storage *Local) Open(ctx context.Context, relativePath string) (application.ReadSeekCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := storage.resolve(relativePath)
	if err != nil {
		return nil, err
	}
	return os.Open(target)
}

func (storage *Local) Delete(ctx context.Context, relativePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := storage.resolve(relativePath)
	if err != nil {
		return err
	}
	err = os.Remove(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (storage *Local) resolve(relativePath string) (string, error) {
	if strings.ContainsAny(relativePath, ":\\\x00") || strings.HasPrefix(relativePath, "/") {
		return "", errors.New("invalid storage path")
	}
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid storage path")
	}
	target := filepath.Join(storage.root, clean)
	relative, err := filepath.Rel(storage.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("storage path escapes data directory")
	}
	current := storage.root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("storage path contains a symbolic link")
		}
	}
	return target, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
