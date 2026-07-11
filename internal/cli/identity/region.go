package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/regions"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack region ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newRegionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "region", Short: "Manage regions"}
	cmd.AddCommand(newRegionListCommand(a, o))
	return cmd
}

func newRegionListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List regions",
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
			return runRegionList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runRegionList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := regions.List(client, regions.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing regions: %w", err)
	}
	all, err := regions.ExtractRegions(pages)
	if err != nil {
		return fmt.Errorf("parsing region list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Parent Region", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		t.Rows = append(t.Rows, []any{r.ID, r.ParentRegionID, r.Description})
	}
	return o.WriteList(w, t)
}
