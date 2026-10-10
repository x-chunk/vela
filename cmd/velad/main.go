// Command velad is the Vela file store server. See "velad --help" and the
// README for deployment.
package main

import (
	"context"
	"os"

	"go.xchunk.org/vela/internal/velad"
)

func main() {
	os.Exit(velad.Main(context.Background(), os.Args[1:]))
}
