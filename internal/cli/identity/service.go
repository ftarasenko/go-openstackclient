package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack service ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "Manage identity catalog services"}
	cmd.AddCommand(
		newServiceListCommand(a, o),
		newServiceShowCommand(a, o),
	)
	return cmd
}

func newServiceListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := services.List(client, services.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing services: %w", err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return fmt.Errorf("parsing service list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Type", "Enabled", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		t.Rows = append(t.Rows, []any{s.ID, s.Name, s.Type, s.Enabled, s.Description})
	}
	return o.WriteList(w, t)
}

func newServiceShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <service>",
		Short: "Show service details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runServiceShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveServiceID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	s, err := services.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing service %q: %w", nameOrID, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Type", "Enabled", "Description"},
		[]any{s.ID, s.Name, s.Type, s.Enabled, s.Description})
}
