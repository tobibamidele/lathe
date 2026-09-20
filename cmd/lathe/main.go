// Command lathe generates Go code and SQL migrations from a Go schema.
package main

import (
	"os"

	"github.com/tobibamidele/lathe/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
