package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.xchunk.org/vela/internal/content"
)

// Backend exposes the streaming contract while retaining local maintenance tools.
type Backend struct{ *Store }

// Put verifies the stream before publishing it in the blob directory.
func (s Backend) Put(ctx context.Context, id string, r io.Reader, opts content.PutOptions) (content.PutResult, error) {
	if opts.MaxSize < 0 || opts.SizeHint > opts.MaxSize {
		return content.PutResult{}, content.ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return content.PutResult{}, err
	}
	p, err := s.Create()
	if err != nil {
		return content.PutResult{}, err
	}
	defer p.Abort()
	stream := content.NewStream(ctx, r, opts.MaxSize)
	if _, err := io.Copy(p, stream); err != nil {
		return content.PutResult{}, fmt.Errorf("write blob: %w", err)
	}
	result := stream.Result()
	if !content.Matches(opts.ExpectSHA256, result.SHA256) {
		return result, content.ErrChecksumMismatch
	}
	if err := ctx.Err(); err != nil {
		return content.PutResult{}, err
	}
	if err := p.Commit(id); err != nil {
		return content.PutResult{}, err
	}
	return result, nil
}

// Open reads just the requested range without loading the file into memory.
func (s Backend) Open(ctx context.Context, id string, off, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if off < 0 {
		return nil, errors.New("negative blob offset")
	}
	f, err := s.Store.Open(id)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	var r io.Reader = f
	if length >= 0 {
		r = io.LimitReader(f, length)
	}
	return &content.Reader{Context: ctx, Source: r, Closer: f}, nil
}

// Delete observes cancellation before removing a blob.
func (s Backend) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Delete(id)
}

// Walk includes sizes needed by migration and contract checks.
func (s Backend) Walk(ctx context.Context, fn func(content.BlobInfo) error) error {
	return s.Store.Walk(ctx, func(id string, modTime time.Time) error {
		f, err := s.Store.Open(id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil {
			return err
		}
		return fn(content.BlobInfo{ID: id, Size: info.Size(), ModTime: modTime})
	})
}

// Check verifies local writability without ignoring a canceled probe.
func (s Backend) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Check()
}

var _ content.Store = Backend{}
