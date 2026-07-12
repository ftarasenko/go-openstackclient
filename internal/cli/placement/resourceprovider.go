package placement

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// providerListFlags holds the filters accepted by "resource provider list".
//
// Flag names follow upstream OSC (`openstack resource provider list`). The
// KeyStack command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
type providerListFlags struct {
	name          string
	uuid          string
	resourceClass string
}

func newProviderListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &providerListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List resource providers",
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
			return runProviderList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by resource provider name")
	fl.StringVar(&f.uuid, "uuid", "", "filter by resource provider UUID")
	// --resource-class maps to placement's "resources" query
	// (e.g. VCPU:1); optional convenience filter.
	fl.StringVar(&f.resourceClass, "resource-class", "", "filter to providers able to serve this resources spec (e.g. VCPU:1)")
	return cmd
}

func runProviderList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *providerListFlags, w io.Writer) error {
	opts := resourceproviders.ListOpts{
		Name:      f.name,
		UUID:      f.uuid,
		Resources: f.resourceClass,
	}
	pages, err := resourceproviders.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing resource providers: %w", err)
	}
	all, err := resourceproviders.ExtractResourceProviders(pages)
	if err != nil {
		return fmt.Errorf("parsing resource provider list: %w", err)
	}
	return o.WriteList(w, providerListTable(all))
}

func providerListTable(list []resourceproviders.ResourceProvider) output.Table {
	cols := []string{"uuid", "name", "generation", "root_provider_uuid", "parent_provider_uuid"}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, p := range list {
		t.Rows = append(t.Rows, []any{p.UUID, p.Name, p.Generation, p.RootProviderUUID, p.ParentProviderUUID})
	}
	return t
}

// providerShowFlags controls "resource provider show".
type providerShowFlags struct {
	allocations bool
}

func newProviderShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &providerShowFlags{}
	cmd := &cobra.Command{
		Use:   "show <uuid>",
		Short: "Show details of a resource provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newPlacementClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderShow(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&f.allocations, "allocations", false, "also fetch and include the provider's allocations")
	return cmd
}

func runProviderShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *providerShowFlags, w io.Writer) error {
	p, err := resourceproviders.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting resource provider %s: %w", id, err)
	}
	fields := []string{"uuid", "name", "generation", "root_provider_uuid", "parent_provider_uuid"}
	values := []any{p.UUID, p.Name, p.Generation, p.RootProviderUUID, p.ParentProviderUUID}

	if f.allocations {
		alloc, err := resourceproviders.GetAllocations(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("getting allocations for resource provider %s: %w", id, err)
		}
		fields = append(fields, "allocations")
		values = append(values, alloc.Allocations)
	}
	return o.WriteSingle(w, fields, values)
}

func newProviderDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <uuid> [<uuid> ...]",
		Short: "Delete resource provider(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newPlacementClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runProviderDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	var errs []error
	for _, id := range ids {
		if err := resourceproviders.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting resource provider %s: %w", id, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted resource provider %s\n", id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newProviderTraitListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <uuid>",
		Short: "List traits associated with a resource provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newPlacementClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderTraitList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runProviderTraitList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	res, err := resourceproviders.GetTraits(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing traits for resource provider %s: %w", id, err)
	}
	t := output.Table{Columns: []string{"name"}, Rows: make([][]any, 0, len(res.Traits))}
	for _, name := range res.Traits {
		t.Rows = append(t.Rows, []any{name})
	}
	return o.WriteList(w, t)
}
