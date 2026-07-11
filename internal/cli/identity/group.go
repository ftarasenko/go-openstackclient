package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/groups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack group ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Manage groups"}
	cmd.AddCommand(newGroupListCommand(a, o))
	return cmd
}

func newGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List groups",
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
			return runGroupList(ctx, client, o, domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain (name or ID)")
	return cmd
}

func runGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, domainNameOrID string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	pages, err := groups.List(client, groups.ListOpts{DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing groups: %w", err)
	}
	all, err := groups.ExtractGroups(pages)
	if err != nil {
		return fmt.Errorf("parsing group list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Domain ID", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, g := range all {
		t.Rows = append(t.Rows, []any{g.ID, g.Name, g.DomainID, g.Description})
	}
	return o.WriteList(w, t)
}
