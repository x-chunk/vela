package blob_test

import (
	"testing"

	"go.xchunk.org/vela/internal/blob"
	"go.xchunk.org/vela/internal/blobtest"
	"go.xchunk.org/vela/internal/content"
)

func TestBackendContract(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) content.Store {
		s, err := blob.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return blob.Backend{Store: s}
	})
}
