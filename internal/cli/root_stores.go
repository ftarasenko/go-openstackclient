//go:build !koc_embedded

package cli

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	s3cli "github.com/ftarasenko/go-openstackclient/internal/cli/s3"
	vaultcli "github.com/ftarasenko/go-openstackclient/internal/cli/vault"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// addStoreCommands registers the two koc-native groups that are not OpenStack
// services: koc s3 and koc vault. A koc_embedded build leaves both out — no
// code, no flags, no help lines — together with the Kubernetes client the s3
// group discovers its credentials through.
func addStoreCommands(root *cobra.Command, a *auth.Options, o *output.Options) {
	root.AddCommand(s3cli.NewCommand(a, o))
	root.AddCommand(vaultcli.NewCommand(a, o))
}
