package placement

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/traits"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// traitListFlags holds the filters accepted by "trait list".
//
// Flag names follow upstream OSC (`openstack trait list`). UNVERIFIED against
// KeyStack (docs.keystack.ru returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.
type traitListFlags struct {
	name       string
	associated bool
}

func newTraitListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &traitListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all traits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newPlacementClient(ctx, a)
			if err != nil {
				return err
			}
			associatedSet := cmd.Flags().Changed("associated")
			return runTraitList(ctx, client, o, f, associatedSet, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by name (supports startswith: and in: operators)")
	fl.BoolVar(&f.associated, "associated", false, "limit to traits associated with at least one resource provider")
	return cmd
}

func runTraitList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *traitListFlags, associatedSet bool, w io.Writer) error {
	opts := traits.ListOpts{Name: f.name}
	if associatedSet {
		opts.Associated = &f.associated
	}
	pages, err := traits.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing traits: %w", err)
	}
	all, err := traits.ExtractTraits(pages)
	if err != nil {
		return fmt.Errorf("parsing trait list: %w", err)
	}
	t := output.Table{Columns: []string{"name"}, Rows: make([][]any, 0, len(all))}
	for _, name := range all {
		t.Rows = append(t.Rows, []any{name})
	}
	return o.WriteList(w, t)
}
