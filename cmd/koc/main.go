// Command koc is a single, statically-linked OpenStack CLI for the KeyStack
// cloud, mirroring the upstream python-openstackclient noun-verb command syntax.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ftarasenko/go-openstackclient/internal/cli"
)

// version is overridden at build time via
// -ldflags "-X main.version=$(git describe --tags --always --dirty)".
var version = "dev"

func main() {
	// Cancel the root context on SIGINT/SIGTERM so in-flight API calls abort
	// promptly and cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCommand(version)
	if err := root.ExecuteContext(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "koc: "+err.Error())
		}
		os.Exit(1)
	}
}
