package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
)

// Exit statuses koc reports, matching what cliff/OSC exits with.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitInterrupted = 130 // 128 + SIGINT's signal number 2
)

// Run executes one koc invocation on a freshly built command tree and returns
// the command cobra resolved args to, with its error.
//
// A fresh tree per invocation is what makes it safe to call more than once in
// a process — `koc shell` calls it per line. cobra and pflag keep flag values
// on the command between Execute calls, so a `--long` on one line would
// otherwise leak into the next; the auth.Options and output.Options the tree
// shares are built with it, so the memoized authentication goes too. The
// process-global state that remains is reset around the run: the resolver memo
// (which --watch enables) and the --timing summary (auth.ReportTiming hands it
// out once).
//
// prepare, when non-nil, sees the tree and the expanded args before execution;
// the shell uses it to apply the scope file and to record what it ran.
func Run(ctx context.Context, version string, args []string, prepare func(root *cobra.Command, args []string)) (*cobra.Command, error) {
	resolve.Reset()
	defer resolve.Reset()

	// NewRootCommand only assembles the tree; ctx reaches every RunE through
	// ExecuteContextC below.
	root := NewRootCommand(version) //nolint:contextcheck // builds commands, makes no calls
	// `openstack` accepts any unambiguous abbreviation of a command name (cliff)
	// and of a long flag (argparse), so muscle memory carries `server li` for
	// `server list`, --all for --all-projects and --fit for --fit-width. Neither
	// cobra nor pflag abbreviates, so the args are normalised before parsing.
	//
	// Command names expand first: flag expansion resolves abbreviations against
	// the flags of the command the args point at, which is only the right command
	// once its name is spelled in full.
	args = ExpandArgs(root, args)
	root.SetArgs(args)
	if prepare != nil {
		prepare(root, args)
	}
	// ExecuteContextC (rather than ExecuteContext) also hands back the command
	// cobra resolved the args to, which is what lets ExitStatus tell "cobra
	// rejected the invocation" from "a command ran and failed": the root's
	// PersistentPreRunE sets SilenceUsage on that command only once cobra's own
	// flag/argument validation has passed, so it is still false whenever
	// validation itself is what failed.
	cmd, err := root.ExecuteContextC(ctx)
	// After the command, and on the failure path too: a slow call is often
	// exactly why it failed. No-op unless --timing was given.
	auth.ReportTiming()
	return cmd, err
}

// ExpandArgs applies the command-name and then the flag-prefix expansion to
// args against root, as Run does before parsing.
func ExpandArgs(root *cobra.Command, args []string) []string {
	return ExpandFlagPrefixes(root, ExpandCommandPrefixes(root, args))
}

// ExitStatus maps the outcome of Run to the process exit status and the line
// to print on stderr ("" for success).
func ExitStatus(cmd *cobra.Command, err error) (int, string) {
	switch {
	case err == nil:
		return ExitOK, ""
	case errors.Is(err, context.Canceled):
		return ExitInterrupted, interruptMessage(err)
	case isUsageError(cmd, err):
		// cobra rejected the invocation itself — unknown flag, wrong argument
		// count, or an unknown noun/verb — the same class of error cliff/OSC
		// exits 2 for.
		return ExitUsage, "koc: " + err.Error()
	default:
		return ExitFailure, "koc: " + err.Error()
	}
}

// interruptMessage says what an interrupt left behind.
//
// Ctrl-C during e.g. `node deploy --wait` leaves the operation running
// server-side, unwatched, so silence there would be actively misleading. Ctrl-C
// during a --watch refresh loop leaves nothing at all — it is how a watch is
// meant to end — so the caveat is dropped rather than printed on every exit
// from a read-only loop.
func interruptMessage(err error) string {
	if errors.Is(err, ErrWatchStopped) {
		return "koc: interrupted"
	}
	return "koc: interrupted (the server-side operation may still be running)"
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
//     it is recognised by the ErrUnknownCommand sentinel that RunE wraps.
func isUsageError(cmd *cobra.Command, err error) bool {
	if cmd != nil && !cmd.SilenceUsage {
		return true
	}
	return errors.Is(err, ErrUnknownCommand)
}
