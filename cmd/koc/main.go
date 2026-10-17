// Command koc is a single, statically-linked OpenStack CLI for the KeyStack
// cloud, mirroring the upstream python-openstackclient noun-verb command syntax.
package main

import (
	"context"
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

	// cli.Run builds the command tree, expands abbreviated command and flag
	// names the way `openstack` accepts them, executes, and prints --timing.
	// The same function runs each line of `koc shell`.
	code, msg := cli.ExitStatus(cli.Run(ctx, version, os.Args[1:], nil))
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if code != cli.ExitOK {
		stop()
		os.Exit(code)
	}
}
