package cli

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/server"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// upstreamSpellingCommands returns the top-level commands that exist only to
// accept the *upstream* spelling of something koc already implements under a
// different word order.
//
// koc nests both console verbs under `server` and the migration verbs under
// `server migration`, but upstream OSC spells them `openstack console log show`
// and — for the cloud-wide listing — with no `server` prefix. Scripts and muscle
// memory carry the upstream form, and cobra cannot attach one command object to
// two parents, so these are freshly-built duplicates of the same constructors
// rather than shared instances. Both spellings therefore run identical code.
//
// The koc-prefixed spellings stay in place; nothing here replaces them.
func upstreamSpellingCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	console := &cobra.Command{
		Use:     "console",
		Short:   "Server console commands (upstream spelling of \"server console\")",
		Aliases: []string{},
	}
	logParent := &cobra.Command{Use: "log", Short: "Server console log"}
	logParent.AddCommand(server.NewConsoleLogShowCommand(a, o))
	urlParent := &cobra.Command{Use: "url", Short: "Server remote console URL"}
	urlParent.AddCommand(server.NewConsoleURLShowCommand(a, o))
	console.AddCommand(logParent, urlParent)

	// `openstack migration list` is the cloud-wide listing; koc's own
	// `server migration list` already treats --server as optional, so this is the
	// same command reached without the prefix. It is hidden because it duplicates
	// a visible sibling exactly.
	migration := &cobra.Command{
		Use:    "migration",
		Short:  "Server migrations (upstream spelling of \"server migration\")",
		Hidden: true,
	}
	migration.AddCommand(server.NewMigrationListCommand(a, o))

	return []*cobra.Command{console, migration}
}
