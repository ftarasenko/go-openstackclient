package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/regions"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack region ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newRegionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "region", Short: "Manage regions"}
	cmd.AddCommand(
		newRegionListCommand(a, o),
		newRegionCreateCommand(a, o),
		newRegionShowCommand(a, o),
		newRegionSetCommand(a, o),
		newRegionDeleteCommand(a, o),
	)
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

// --- create -----------------------------------------------------------------

func newRegionCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var parent, description string
	cmd := &cobra.Command{
		Use:   "create [<region-id>]",
		Short: "Create a new region",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			var id string
			if len(args) == 1 {
				id = args[0]
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runRegionCreate(ctx, client, o, id, parent, description, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&parent, "parent-region", "", "ID of the parent region")
	fl.StringVar(&description, "description", "", "description of the region")
	return cmd
}

// runRegionCreate creates a region. The ID is optional because keystone
// generates a UUID when it is omitted — regions are the one identity resource
// whose primary key the caller may choose, which is why upstream takes it as a
// positional rather than a --name.
func runRegionCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, parent, description string, w io.Writer,
) error {
	r, err := regions.Create(ctx, client, regions.CreateOpts{
		ID:             id,
		ParentRegionID: parent,
		Description:    description,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating region %q: %w", id, err)
	}
	return writeRegion(o, w, r)
}

func writeRegion(o *output.Options, w io.Writer, r *regions.Region) error {
	return o.WriteSingle(w,
		[]string{"ID", "Parent Region", "Description"},
		[]any{r.ID, r.ParentRegionID, r.Description})
}

// --- show / delete / set ----------------------------------------------------

func newRegionShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <region-id>",
		Short: "Show region details",
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
			return runRegionShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runRegionShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	r, err := regions.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing region %q: %w", id, err)
	}
	return writeRegion(o, w, r)
}

func newRegionDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <region-id> [<region-id> ...]",
		Short: "Delete region(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runRegionDelete(ctx, client, args)
		},
	}
}

func runRegionDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := regions.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting region %q: %w", id, err)
		}
		return nil
	})
}

func newRegionSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var parent, description string
	cmd := &cobra.Command{
		Use:   "set <region-id>",
		Short: "Set region properties",
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
			return runRegionSet(ctx, client, args[0], parent, description, cmd.Flags().Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&parent, "parent-region", "", "ID of the parent region")
	fl.StringVar(&description, "description", "", "new description")
	return cmd
}

func runRegionSet(ctx context.Context, client *gophercloud.ServiceClient, id, parent, description string, descSet bool) error {
	opts := regions.UpdateOpts{ParentRegionID: parent}
	if descSet {
		opts.Description = &description
	}
	if _, err := regions.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating region %q: %w", id, err)
	}
	return nil
}
