// Package cli assembles the koc command tree: the cobra root, the shared global
// flags (auth/TLS/microversion + output), and each service's command group.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/baremetal"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewRootCommand wires the global flags and every service command group. The
// authenticated ProviderClient is built lazily inside each command's RunE, once
// per invocation, so `--help` and validation never require credentials.
func NewRootCommand(version string) *cobra.Command {
	authOpts := &auth.Options{}
	outOpts := &output.Options{}

	root := &cobra.Command{
		Use:           "koc",
		Short:         "koc — a single-binary OpenStack CLI for KeyStack",
		Long:          "koc is a statically-linked Go replacement for python-openstackclient,\nmirroring the upstream `openstack` noun-verb command syntax.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	authOpts.AddFlags(pf)
	outOpts.AddFlags(pf)

	root.SetVersionTemplate(fmt.Sprintf("koc %s\n", version))

	root.AddCommand(baremetal.NewCommand(authOpts, outOpts))

	return root
}
