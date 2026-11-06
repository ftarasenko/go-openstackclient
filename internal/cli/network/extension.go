package network

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	"github.com/ftarasenko/koc/pkg/openstack/extension"
)

// The discovery calls are pkg/openstack/extension's, which nova, cinder and
// neutron share. These wrappers hand back gophercloud's error unwrapped, so
// each caller words it as it always has.

type networkExtension = extension.Extension

func listNetworkExtensions(ctx context.Context, client *gophercloud.ServiceClient) ([]networkExtension, error) {
	all, err := extension.List(ctx, client)
	return all, errors.Unwrap(err)
}

func getNetworkExtension(ctx context.Context, client *gophercloud.ServiceClient, alias string) (*networkExtension, error) {
	e, err := extension.Get(ctx, client, alias)
	return e, errors.Unwrap(err)
}

// newExtensionCommand builds "network extension ...".
//
// Command names follow upstream OSC (`openstack extension list --network`, which
// koc spells `network extension list` to keep it under the service it queries).
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time).
func newExtensionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extension",
		Short: "Inspect the API extensions this neutron endpoint enables",
	}
	cmd.AddCommand(
		newExtensionListCommand(a, o),
		newExtensionShowCommand(a, o),
	)
	return cmd
}

func newExtensionListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List enabled network API extensions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runExtensionList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "include each extension's description")
	return cmd
}

func runExtensionList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	all, err := listNetworkExtensions(ctx, client)
	if err != nil {
		return fmt.Errorf("listing network extensions: %w", err)
	}
	cols := []string{"Name", "Alias"}
	if long {
		cols = append(cols, "Description", "Updated")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, e := range all {
		row := []any{e.Name, e.Alias}
		if long {
			row = append(row, e.Description, e.Updated)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newExtensionShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <alias>",
		Short: "Show one network API extension by alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runExtensionShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runExtensionShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, alias string, w io.Writer) error {
	e, err := getNetworkExtension(ctx, client, alias)
	if err != nil {
		return fmt.Errorf("showing network extension %q: %w", alias, err)
	}
	return o.WriteSingle(w,
		[]string{"name", "alias", "description", "updated"},
		[]any{e.Name, e.Alias, e.Description, e.Updated})
}
