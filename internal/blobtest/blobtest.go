// Package blobtest exercises the same immutable-content contract for every backend.
package blobtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
)

// Run verifies streaming writes, cleanup, ranges, discovery, and cancellation.
// Each subtest gets an isolated store so it can assert the absence of garbage.
func Run(t *testing.T, newStore func(*testing.T) content.Store) {
	t.Helper()
	t.Run("roundtrip", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		id := ids.NewULID(nil).New()
		data := bytes.Repeat([]byte("0123456789"), 1<<20)
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		got, err := s.Put(ctx, id, bytes.NewReader(data), content.PutOptions{MaxSize: int64(len(data)), SizeHint: -1, ExpectSHA256: digest})
		if err != nil || got.Size != int64(len(data)) || got.SHA256 != digest {
			t.Fatalf("put: %+v %v", got, err)
		}
		for _, r := range []struct{ off, len int64 }{{0, -1}, {2, 3}, {int64(len(data) - 5), -1}, {0, 0}} {
			f, err := s.Open(ctx, id, r.off, r.len)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			end := int64(len(data))
			if r.len >= 0 {
				end = r.off + r.len
			}
			if !bytes.Equal(b, data[r.off:end]) {
				t.Fatal("range differs")
			}
		}
		found := false
		if err := s.Walk(ctx, func(i content.BlobInfo) error {
			if i.ID == id {
				found = i.Size == int64(len(data)) && !i.ModTime.IsZero()
			}
			return nil
		}); err != nil || !found {
			t.Fatalf("walk: %v found=%v", err, found)
		}
		if err := s.Check(ctx); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := s.Delete(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Open(ctx, id, 0, -1); !errors.Is(err, content.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
	})
	for _, test := range []struct {
		name   string
		reader func(context.CancelFunc) io.Reader
		opts   content.PutOptions
		want   error
	}{
		{"limit", func(context.CancelFunc) io.Reader { return strings.NewReader("too long") }, content.PutOptions{MaxSize: 3, SizeHint: -1}, content.ErrTooLarge},
		{"checksum", func(context.CancelFunc) io.Reader { return strings.NewReader("x") }, content.PutOptions{MaxSize: 10, SizeHint: -1, ExpectSHA256: strings.Repeat("0", 64)}, content.ErrChecksumMismatch},
		{"read-error", func(context.CancelFunc) io.Reader {
			return io.MultiReader(strings.NewReader("partial"), failedReader{})
		}, content.PutOptions{MaxSize: 100, SizeHint: -1}, errSource},
		{"cancel", func(cancel context.CancelFunc) io.Reader { return &cancelReader{cancel: cancel} }, content.PutOptions{MaxSize: 100, SizeHint: -1}, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id := ids.NewULID(nil).New()
			_, err := s.Put(ctx, id, test.reader(cancel), test.opts)
			if !errors.Is(err, test.want) {
				t.Fatalf("put: %v want %v", err, test.want)
			}
			ctx = context.Background()
			if f, err := s.Open(ctx, id, 0, -1); err == nil {
				_ = f.Close()
				t.Fatal("failed upload visible")
			}
			n := 0
			if err := s.Walk(ctx, func(content.BlobInfo) error { n++; return nil }); err != nil || n != 0 {
				t.Fatalf("garbage: %d %v", n, err)
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		id := ids.NewULID(nil).New()
		result, err := s.Put(ctx, id, strings.NewReader(""), content.PutOptions{MaxSize: 0, SizeHint: 0})
		if err != nil || result.Size != 0 {
			t.Fatalf("empty: %+v %v", result, err)
		}
		f, err := s.Open(ctx, id, 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || len(b) != 0 {
			t.Fatalf("empty read: %q %v", b, err)
		}
	})
}

var errSource = errors.New("source failed")

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errSource }

type cancelReader struct {
	cancel context.CancelFunc
	done   bool
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.cancel()
	return copy(p, "partial"), nil
}
