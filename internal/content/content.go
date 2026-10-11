// Package content defines the backend-independent contract for immutable blobs.
// It contains no transport, database, or filesystem policy.
package content

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"
)

// Backend errors are shared so callers need not inspect implementation errors.
var (
	ErrNotFound         = errors.New("blob not found")
	ErrTooLarge         = errors.New("blob exceeds size limit")
	ErrChecksumMismatch = errors.New("blob checksum mismatch")
)

// PutOptions bounds an upload and optionally verifies its digest.
type PutOptions struct {
	MaxSize      int64
	SizeHint     int64
	ContentType  string
	ExpectSHA256 string
}

// PutResult describes the bytes actually persisted.
type PutResult struct {
	Size   int64
	SHA256 string
}

// BlobInfo describes a stored blob for orphan discovery.
type BlobInfo struct {
	ID      string
	Size    int64
	ModTime time.Time
}

// Store persists immutable contents under opaque IDs, never logical paths.
// Callers allocate a fresh ID for every write. Failed writes leave no content.
type Store interface {
	Put(context.Context, string, io.Reader, PutOptions) (PutResult, error)
	Open(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Walk(context.Context, func(BlobInfo) error) error
	Check(context.Context) error
}

// Stream checks cancellation and size while hashing the consumed bytes.
// Keeping it a Reader (not a ReadSeeker) bounds multipart uploader buffering.
type Stream struct {
	ctx context.Context
	r   io.Reader
	h   interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	max, size int64
	err       error
}

// NewStream wraps r with a hard size limit; zero permits only empty content.
func NewStream(ctx context.Context, r io.Reader, max int64) *Stream {
	return &Stream{ctx: ctx, r: r, max: max, h: sha256.New()}
}

// Read hashes only bytes within the limit and preserves source failures.
func (s *Stream) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		s.err = err
		return 0, err
	}
	if s.err != nil {
		return 0, s.err
	}
	if int64(len(p)) > s.max-s.size {
		p = p[:min(int64(len(p)), s.max-s.size+1)]
	}
	n, err := s.r.Read(p)
	if int64(n) > s.max-s.size {
		s.err = ErrTooLarge
		return 0, s.err
	}
	s.size += int64(n)
	_, _ = s.h.Write(p[:n])
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
	}
	return n, err
}

// Result returns the size and SHA-256 of the consumed stream.
func (s *Stream) Result() PutResult {
	return PutResult{Size: s.size, SHA256: hex.EncodeToString(s.h.Sum(nil))}
}

// Err returns the original read failure, even if a transport wraps it.
func (s *Stream) Err() error { return s.err }

// Matches compares a client digest in constant time.
func Matches(expected, actual string) bool {
	return expected == "" || subtle.ConstantTimeCompare([]byte(strings.ToLower(expected)), []byte(actual)) == 1
}

// Reader checks context cancellation for local reads and owns its closer.
type Reader struct {
	Context context.Context
	Source  io.Reader
	Closer  io.Closer
}

// Read observes cancellation before touching the underlying reader.
func (r *Reader) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Source.Read(p)
}

// Close releases the underlying resource.
func (r *Reader) Close() error { return r.Closer.Close() }
