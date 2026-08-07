package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// groupAnnotation marks a command that exists only to group subcommands. Such a
// command is given a RunE by requireSubcommands (so cobra reaches argument
// handling at all), which would otherwise make it look runnable in `--help`; the
// annotation lets the usage template keep printing the group form only.
const groupAnnotation = "koc_group"

// requireSubcommands makes every group-only command in the tree reject an
// unknown or extra argument with a non-zero exit, instead of cobra's default of
// printing help to stdout and exiting 0. Scripts could not otherwise tell
// `koc zone list` from the typo `koc zone lst`.
//
// Upstream behaves this way too: cliff raises ValueError("Unknown command") and
// cliff/app.py exits 2.
//
// A group command is one with subcommands and no Run/RunE of its own. cobra
// returns flag.ErrHelp for those *before* it validates arguments (see
// Command.execute), so an Args validator would never run — the group needs a
// RunE. Bare `koc zone` with no argument stays a help request, as it is today.
func requireSubcommands(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		requireSubcommands(sub)
	}
	if cmd.Runnable() || !cmd.HasSubCommands() {
		return
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[groupAnnotation] = "true"
	// Accept any arguments here so cobra hands them to RunE rather than
	// rejecting them with its own message; RunE decides.
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return c.Help()
		}
		// Past flag/arg validation, so the usage block would be noise: the
		// message already names the command and points at --help.
		c.SilenceUsage = true
		return fmt.Errorf("unknown command %q for %q\nRun '%s --help' for the available commands",
			args[0], c.CommandPath(), c.CommandPath())
	}
}
