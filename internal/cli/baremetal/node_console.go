package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The node states sub-resources gophercloud's nodes package does not model:
// the serial/graphical console (/states/console) and secure boot
// (/states/secure_boot, API 1.76). Both are raw calls.

// newNodeConsoleCommand builds "baremetal node console show|enable|disable".
func newNodeConsoleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "console",
		Short: "Manage a node's console",
	}
	show := &cobra.Command{
		Use:   "show <node>",
		Short: "Show a node's console information",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeConsoleShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(show,
		newNodeStateToggleCommand(a, "enable", "Enable the console of node(s)", "console", true),
		newNodeStateToggleCommand(a, "disable", "Disable the console of node(s)", "console", false),
	)
	return cmd
}

func runNodeConsoleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, node string, w io.Writer) error {
	var body map[string]any
	if err := ironicGet(ctx, client, joinPath("nodes", node, "states", "console"), &body); err != nil {
		return fmt.Errorf("getting console of baremetal node %s: %w", node, err)
	}
	fields, values := showFields(body, []string{"console_enabled", "console_info"})
	return o.WriteSingle(w, fields, values)
}

// newNodeSecureBootCommand builds "baremetal node secure boot on|off".
func newNodeSecureBootCommand(a *auth.Options) *cobra.Command {
	boot := &cobra.Command{
		Use:   "boot",
		Short: "Turn a node's UEFI secure boot on or off",
	}
	boot.AddCommand(
		newNodeStateToggleCommand(a, "on", "Turn secure boot on for node(s)", "secure_boot", true),
		newNodeStateToggleCommand(a, "off", "Turn secure boot off for node(s)", "secure_boot", false),
	)
	secure := &cobra.Command{
		Use:   "secure",
		Short: "Manage a node's secure boot",
	}
	secure.AddCommand(boot)
	return secure
}

// newNodeStateToggleCommand builds a verb that PUTs a boolean to
// /nodes/<node>/states/<state> for each node named.
func newNodeStateToggleCommand(a *auth.Options, verb, short, state string, on bool) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <node> [<node> ...]",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeStateToggle(ctx, client, args, state, on, cmd.OutOrStdout())
		},
	}
}

// runNodeStateToggle sets a boolean node state on each node. The console
// takes {"enabled": b}, secure boot {"target": b}; both answer 202 and finish
// asynchronously in the conductor.
func runNodeStateToggle(ctx context.Context, client *gophercloud.ServiceClient, nodes []string, state string, on bool, w io.Writer) error {
	key, what := "enabled", "console"
	if state == "secure_boot" {
		key, what = "target", "secure boot"
	}
	verb := map[bool]string{true: "on", false: "off"}[on]
	return batchdelete.Each(nodes, func(node string) error {
		if err := ironicPut(ctx, client, joinPath("nodes", node, "states", state), map[string]any{key: on}); err != nil {
			return fmt.Errorf("turning %s %s for baremetal node %s: %w", what, verb, node, err)
		}
		_, err := fmt.Fprintf(w, "Requested %s %s for node %s\n", what, verb, node)
		return err
	})
}
