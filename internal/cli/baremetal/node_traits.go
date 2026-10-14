package baremetal

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Node traits (API 1.37) are what nova's flavor `trait:CUSTOM_X=required`
// schedules on, and what deploy templates match. gophercloud's nodes package
// has no traits calls, so these use the raw helpers.
//
// Upstream registers each verb under two spellings, `node trait add` and
// `node add trait`; both are entry points of python-ironicclient, so both are
// commands here.

// newNodeTraitCommand builds "baremetal node trait list|add|remove".
func newNodeTraitCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trait",
		Short: "Manage a node's traits",
	}
	cmd.AddCommand(
		newNodeTraitListCommand(a, o),
		newNodeTraitAddCommand(a, "add <node> <trait> [<trait> ...]"),
		newNodeTraitRemoveCommand(a, "remove <node> [<trait> ...]"),
	)
	return cmd
}

// newNodeAddRemoveTraitCommands builds upstream's other spelling, "baremetal
// node add trait" and "baremetal node remove trait".
func newNodeAddRemoveTraitCommands(a *auth.Options) []*cobra.Command {
	add := &cobra.Command{Use: "add", Short: "Add traits to a node (same as \"node trait add\")"}
	add.AddCommand(newNodeTraitAddCommand(a, "trait <node> <trait> [<trait> ...]"))
	remove := &cobra.Command{Use: "remove", Short: "Remove traits from a node (same as \"node trait remove\")"}
	remove.AddCommand(newNodeTraitRemoveCommand(a, "trait <node> [<trait> ...]"))
	return []*cobra.Command{add, remove}
}

func newNodeTraitListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   useListNode,
		Short: "List a node's traits",
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
			return runNodeTraitList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runNodeTraitList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, node string, w io.Writer) error {
	var body struct {
		Traits []string `json:"traits"`
	}
	if err := ironicGet(ctx, client, joinPath("nodes", node, "traits"), &body); err != nil {
		return fmt.Errorf("listing traits of baremetal node %s: %w", node, err)
	}
	t := output.Table{Columns: []string{"Traits"}, Rows: make([][]any, 0, len(body.Traits))}
	for _, tr := range body.Traits {
		t.Rows = append(t.Rows, []any{tr})
	}
	return o.WriteList(w, t)
}

func newNodeTraitAddCommand(a *auth.Options, use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: "Add traits to a node",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeTraitAdd(ctx, client, args[0], args[1:], cmd.OutOrStdout())
		},
	}
}

// runNodeTraitAdd adds each trait with its own request, as upstream does, so
// one invalid trait does not stop the others.
func runNodeTraitAdd(ctx context.Context, client *gophercloud.ServiceClient, node string, traits []string, w io.Writer) error {
	var errs []error
	for _, tr := range traits {
		if err := ironicPut(ctx, client, joinPath("nodes", node, "traits", tr), nil); err != nil {
			errs = append(errs, fmt.Errorf("failed to add trait %s: %w", tr, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Added trait %s\n", tr); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

func newNodeTraitRemoveCommand(a *auth.Options, use string) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "Remove traits from a node",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 1) {
				return errors.New("name the trait(s) to remove, or pass --all")
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeTraitRemove(ctx, client, args[0], args[1:], all, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "remove all traits")
	return cmd
}

func runNodeTraitRemove(ctx context.Context, client *gophercloud.ServiceClient, node string, traits []string, all bool, w io.Writer) error {
	if all {
		if err := ironicDelete(ctx, client, joinPath("nodes", node, "traits")); err != nil {
			return fmt.Errorf("removing all traits of baremetal node %s: %w", node, err)
		}
		return nil
	}
	var errs []error
	for _, tr := range traits {
		if err := ironicDelete(ctx, client, joinPath("nodes", node, "traits", tr)); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove trait %s: %w", tr, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Removed trait %s\n", tr); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}
