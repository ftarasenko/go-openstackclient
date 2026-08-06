package placement

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// This file adds the read side of a resource provider's three subresources —
// inventories, usages and aggregates — all served by typed gophercloud calls.
//
// Command names follow upstream osc-placement (`openstack resource provider
// inventory list`, `... usage show`, `... aggregate list`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream osc-placement semantics.

// newProviderInventoryCommand builds "resource provider inventory ...".
func newProviderInventoryCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Inspect a resource provider's inventories",
	}
	cmd.AddCommand(newProviderInventoryListCommand(a, o))
	cmd.AddCommand(newProviderInventoryShowCommand(a, o))
	return cmd
}

func newProviderInventoryListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <uuid>",
		Short: "List a resource provider's inventories, one row per resource class",
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
			return runProviderInventoryList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runProviderInventoryList renders the inventories map as one row per resource
// class. The API returns an object keyed by class, so rows are sorted by class
// name to keep the output stable between invocations.
func runProviderInventoryList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	res, err := resourceproviders.GetInventories(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing inventories for resource provider %s: %w", id, err)
	}
	classes := make([]string, 0, len(res.Inventories))
	for class := range res.Inventories {
		classes = append(classes, class)
	}
	sort.Strings(classes)

	t := output.Table{
		Columns: []string{
			"resource_class", "total", "reserved", "min_unit", "max_unit",
			"step_size", "allocation_ratio",
		},
		Rows: make([][]any, 0, len(classes)),
	}
	for _, class := range classes {
		i := res.Inventories[class]
		t.Rows = append(t.Rows, []any{
			class, i.Total, i.Reserved, i.MinUnit, i.MaxUnit, i.StepSize, i.AllocationRatio,
		})
	}
	return o.WriteList(w, t)
}

func newProviderInventoryShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <uuid> <resource-class>",
		Short: "Show one resource class's inventory on a resource provider",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newPlacementClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderInventoryShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runProviderInventoryShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id, class string, w io.Writer) error {
	res, err := resourceproviders.GetInventory(ctx, client, id, class).Extract()
	if err != nil {
		return fmt.Errorf("getting %s inventory for resource provider %s: %w", class, id, err)
	}
	return o.WriteSingle(w,
		[]string{"total", "reserved", "min_unit", "max_unit", "step_size", "allocation_ratio", "resource_provider_generation"},
		[]any{res.Total, res.Reserved, res.MinUnit, res.MaxUnit, res.StepSize, res.AllocationRatio, res.ResourceProviderGeneration})
}

// newProviderUsageCommand builds "resource provider usage ...".
func newProviderUsageCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Inspect a resource provider's usages",
	}
	cmd.AddCommand(newProviderUsageShowCommand(a, o))
	return cmd
}

func newProviderUsageShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <uuid>",
		Short: "Show a resource provider's usage per resource class",
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
			return runProviderUsageShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runProviderUsageShow renders usages as one row per resource class, matching
// osc-placement, which prints a resource_class/usage table rather than a single
// wide record.
func runProviderUsageShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	res, err := resourceproviders.GetUsages(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting usages for resource provider %s: %w", id, err)
	}
	classes := make([]string, 0, len(res.Usages))
	for class := range res.Usages {
		classes = append(classes, class)
	}
	sort.Strings(classes)

	t := output.Table{Columns: []string{"resource_class", "usage"}, Rows: make([][]any, 0, len(classes))}
	for _, class := range classes {
		t.Rows = append(t.Rows, []any{class, res.Usages[class]})
	}
	return o.WriteList(w, t)
}

// newProviderAggregateCommand builds "resource provider aggregate ...".
func newProviderAggregateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Inspect a resource provider's aggregates",
	}
	cmd.AddCommand(newProviderAggregateListCommand(a, o))
	return cmd
}

func newProviderAggregateListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <uuid>",
		Short: "List the aggregates a resource provider belongs to",
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
			return runProviderAggregateList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runProviderAggregateList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	res, err := resourceproviders.GetAggregates(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing aggregates for resource provider %s: %w", id, err)
	}
	t := output.Table{Columns: []string{"uuid"}, Rows: make([][]any, 0, len(res.Aggregates))}
	for _, uuid := range res.Aggregates {
		t.Rows = append(t.Rows, []any{uuid})
	}
	return o.WriteList(w, t)
}
