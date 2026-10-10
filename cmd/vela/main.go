// Command vela is the command line client of the Vela file store. Run
// "vela --help", and see the README for a user manual.
package main

import (
	"os"

	"go.xchunk.org/vela/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
