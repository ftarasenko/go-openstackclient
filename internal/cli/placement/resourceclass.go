package placement

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceclasses"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "resource class" — placement's inventory categories (VCPU, MEMORY_MB and any
// CUSTOM_* the operator defines).
//
// Verb and flag names mirror upstream osc-placement. UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream semantics.

func newResourceClassCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "class", Short: "Manage placement resource classes"}
	cmd.AddCommand(
		newResourceClassListCommand(a, o),
		newResourceClassShowCommand(a, o),
		newResourceClassCreateCommand(a, o),
		newResourceClassSetCommand(a, o),
		newResourceClassDeleteCommand(a, o),
	)
	return cmd
}

func newResourceClassListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List resource classes",
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
			return runResourceClassList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runResourceClassList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := resourceclasses.List(client).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing resource classes: %w", err)
	}
	all, err := resourceclasses.ExtractResourceClasses(pages)
	if err != nil {
		return fmt.Errorf("parsing the resource class list: %w", err)
	}
	t := output.Table{Columns: []string{"Name"}, Rows: make([][]any, 0, len(all))}
	for _, rc := range all {
		t.Rows = append(t.Rows, []any{rc.Name})
	}
	return o.WriteList(w, t)
}

func newResourceClassShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a resource class",
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
			return runResourceClassShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runResourceClassShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	rc, err := resourceclasses.Get(ctx, client, name).Extract()
	if err != nil {
		return fmt.Errorf("showing resource class %s: %w", name, err)
	}
	return o.WriteSingle(w, []string{"name"}, []any{rc.Name})
}

func newResourceClassCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a resource class",
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
			return runResourceClassCreate(ctx, client, args[0])
		},
	}
}

// runResourceClassCreate creates a class. Placement answers 201 with an empty
// body and a Location header, so there is nothing to render — success is the
// absence of an error, matching osc-placement.
func runResourceClassCreate(ctx context.Context, client *gophercloud.ServiceClient, name string) error {
	if err := resourceclasses.Create(ctx, client, resourceclasses.CreateOpts{Name: name}).ExtractErr(); err != nil {
		return fmt.Errorf("creating resource class %s: %w", name, err)
	}
	return nil
}

func newResourceClassSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Create a resource class if it does not exist",
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
			return runResourceClassSet(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runResourceClassSet mirrors osc-placement's `resource class set`, which is an
// idempotent create rather than a rename: placement's PUT /resource_classes/
// <name> creates the class when it is missing and is a no-op when it exists.
func runResourceClassSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	if err := resourceclasses.Update(ctx, client, name).ExtractErr(); err != nil {
		return fmt.Errorf("setting resource class %s: %w", name, err)
	}
	// Placement answers the PUT with 200 and no useful body, so the name is
	// echoed back rather than read from the response.
	return o.WriteSingle(w, []string{"name"}, []any{name})
}

func newResourceClassDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name> [<name> ...]",
		Short: "Delete resource class(es)",
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
			return runResourceClassDelete(ctx, client, args)
		},
	}
}

func runResourceClassDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string) error {
	return batchdelete.Each(names, func(name string) error {
		if err := resourceclasses.Delete(ctx, client, name).ExtractErr(); err != nil {
			return fmt.Errorf("deleting resource class %s: %w", name, err)
		}
		return nil
	})
}
