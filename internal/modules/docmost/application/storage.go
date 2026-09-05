package application

import (
	"context"
	"io"
)

var ErrUploadTooLarge = errUploadTooLarge{}

type errUploadTooLarge struct{}

func (errUploadTooLarge) Error() string { return "upload exceeds configured size limit" }

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type Storage interface {
	Save(ctx context.Context, relativePath string, source io.Reader, maxBytes int64) (int64, error)
	Open(ctx context.Context, relativePath string) (ReadSeekCloser, error)
	Delete(ctx context.Context, relativePath string) error
}
