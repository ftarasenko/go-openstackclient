package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/cli/shell"
)

// newShellCommand is `koc shell`. The shell package cannot import this one —
// this one builds the tree the shell is part of — so it is handed the
// functions it runs each line with.
func newShellCommand(version string) *cobra.Command {
	return shell.NewCommand(shellConfig(version))
}

func shellConfig(version string) shell.Config {
	return shell.Config{
		Run: func(ctx context.Context, args []string, prepare func(*cobra.Command, []string)) (*cobra.Command, error) {
			return Run(ctx, version, args, prepare)
		},
		ExitStatus: ExitStatus,
		NewRoot:    func() *cobra.Command { return NewRootCommand(version) },
		Expand:     ExpandArgs,
	}
}
