// Command hkcli is an unofficial command-line client for Hike Messenger.
package main

import (
	"os"

	"github.com/vinaycodeaxe/hkcli/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
