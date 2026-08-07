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

	"github.com/ftarasenko/go-openstackclient/internal/auth"
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
	// `openstack` accepts any unambiguous abbreviation of a command name (cliff)
	// and of a long flag (argparse), so muscle memory carries `server li` for
	// `server list`, --all for --all-projects and --fit for --fit-width. Neither
	// cobra nor pflag abbreviates, so the args are normalised before parsing.
	//
	// Command names expand first: flag expansion resolves abbreviations against
	// the flags of the command the args point at, which is only the right command
	// once its name is spelled in full.
	args := cli.ExpandCommandPrefixes(root, os.Args[1:])
	root.SetArgs(cli.ExpandFlagPrefixes(root, args))
	err := root.ExecuteContext(ctx)
	// After the command, and on the failure path too: a slow call is often
	// exactly why it failed. No-op unless --timing was given.
	auth.ReportTiming()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "koc: "+err.Error())
		}
		os.Exit(1)
	}
}
