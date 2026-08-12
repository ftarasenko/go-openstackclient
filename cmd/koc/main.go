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

	"github.com/spf13/cobra"

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
	// ExecuteContextC (rather than ExecuteContext) also hands back the command
	// cobra resolved the args to, which is what lets the exit-code switch below
	// tell "cobra rejected the invocation" from "a command ran and failed"
	// without touching root.go or groups.go: PersistentPreRunE (root.go) sets
	// SilenceUsage on that command only once cobra's own flag/argument
	// validation has passed, so it is still false whenever validation itself
	// is what failed.
	cmd, err := root.ExecuteContextC(ctx)
	// After the command, and on the failure path too: a slow call is often
	// exactly why it failed. No-op unless --timing was given.
	auth.ReportTiming()
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled):
		// Ctrl-C during e.g. `node deploy --wait`: the operation itself keeps
		// running server-side, unwatched, so silence here would be actively
		// misleading. 130 is the conventional "killed by SIGINT" exit status
		// (128 + SIGINT's signal number 2).
		fmt.Fprintln(os.Stderr, "koc: interrupted (the server-side operation may still be running)")
		os.Exit(130)
	case isUsageError(cmd, err):
		// cobra rejected the invocation itself — unknown flag, wrong argument
		// count, or an unknown noun/verb — the same class of error cliff/OSC
		// exits 2 for.
		fmt.Fprintln(os.Stderr, "koc: "+err.Error())
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "koc: "+err.Error())
		os.Exit(1)
	}
}

// isUsageError reports whether err comes from cobra rejecting the invocation
// rather than from a command that actually ran.
//
// Two distinct signals are needed, because koc's command groups (see
// groups.go's requireSubcommands, including the root command itself: it is a
// pure dispatcher with no RunE of its own before that pass runs) surface an
// unknown verb from their own RunE rather than from cobra's usual pre-RunE
// validation:
//
//   - An unknown flag or a bad argument count on a real leaf command is caught
//     by cobra before PersistentPreRunE ever runs, so SilenceUsage — set on cmd
//     by root.go's PersistentPreRunE once validation has passed — is still
//     false.
//   - An unknown noun/verb under a group (including a top-level noun, since
//     the root is itself such a group) is instead reported by that group's own
//     RunE — after PersistentPreRunE already flipped SilenceUsage to true — so
//     it is recognised by the cli.ErrUnknownCommand sentinel that RunE wraps.
func isUsageError(cmd *cobra.Command, err error) bool {
	if cmd != nil && !cmd.SilenceUsage {
		return true
	}
	return errors.Is(err, cli.ErrUnknownCommand)
}
