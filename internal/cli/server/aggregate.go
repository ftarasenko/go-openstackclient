package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newAggregateCommand builds the "aggregate" command group, mirroring the
// upstream `openstack aggregate ...` (nova host-aggregates) surface. Host
// aggregates are a compute (nova) resource, so every leaf uses the shared
// compute client helper.
func newAggregateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Host aggregate (nova) commands",
	}
	// "aggregate add host" / "aggregate remove host" are two-word OSC nouns,
	// modeled as nested parents so cobra resolves them unambiguously.
	add := &cobra.Command{Use: "add", Short: "Add a resource to an aggregate"}
	add.AddCommand(newAggregateAddHostCommand(a, o))
	remove := &cobra.Command{Use: "remove", Short: "Remove a resource from an aggregate"}
	remove.AddCommand(newAggregateRemoveHostCommand(a, o))
	cmd.AddCommand(
		newAggregateListCommand(a, o),
		newAggregateShowCommand(a, o),
		newAggregateCreateCommand(a, o),
		newAggregateDeleteCommand(a, o),
		newAggregateSetCommand(a, o),
		newAggregateUnsetCommand(a, o),
		add,
		remove,
	)
	return cmd
}

// resolveAggregateID accepts either a numeric aggregate ID or an aggregate name
// and returns the numeric ID the nova API keys on. A value that parses as an
// integer is used verbatim; otherwise the aggregate list is consulted and the
// name matched (erroring on zero or multiple matches).
func resolveAggregateID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (int, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		return id, nil
	}
	all, err := listAggregates(ctx, client)
	if err != nil {
		return 0, err
	}
	var matches []int
	for _, agg := range all {
		if agg.Name == ref {
			matches = append(matches, agg.ID)
		}
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("no aggregate found with name %q", ref)
	case 1:
		return matches[0], nil
	default:
		return 0, fmt.Errorf("more than one aggregate named %q; specify the ID", ref)
	}
}

func listAggregates(ctx context.Context, client *gophercloud.ServiceClient) ([]aggregates.Aggregate, error) {
	pages, err := aggregates.List(client).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing aggregates: %w", err)
	}
	all, err := aggregates.ExtractAggregates(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing aggregate list: %w", err)
	}
	return all, nil
}

func newAggregateListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List host aggregates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

func runAggregateList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	all, err := listAggregates(ctx, client)
	if err != nil {
		return err
	}
	cols := []string{"ID", "Name", "Availability Zone"}
	if long {
		cols = append(cols, "Hosts", "Properties")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for i := range all {
		agg := &all[i]
		row := []any{agg.ID, agg.Name, agg.AvailabilityZone}
		if long {
			row = append(row, strings.Join(agg.Hosts, ", "), formatAggregateMetadata(agg.Metadata))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newAggregateShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <aggregate>",
		Short: "Show details of a host aggregate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runAggregateShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	agg, err := aggregates.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing aggregate %q: %w", ref, err)
	}
	fields, values := aggregateShowFields(agg)
	return o.WriteSingle(w, fields, values)
}

func aggregateShowFields(agg *aggregates.Aggregate) ([]string, []any) {
	fields := []string{"ID", "Name", "Availability Zone", "Hosts", "Properties", "UUID", "Created At", "Updated At"}
	values := []any{
		agg.ID, agg.Name, agg.AvailabilityZone, strings.Join(agg.Hosts, ", "),
		formatAggregateMetadata(agg.Metadata), agg.UUID,
		agg.CreatedAt.String(), agg.UpdatedAt.String(),
	}
	return fields, values
}

// formatAggregateMetadata renders the aggregate metadata map as a stable,
// comma-separated "key='value'" string, matching OSC's Properties column.
func formatAggregateMetadata(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s='%s'", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

type aggregateCreateFlags struct {
	zone       string
	properties []string
}

func newAggregateCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &aggregateCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new host aggregate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.zone, "zone", "", "availability zone for the aggregate")
	fl.StringArrayVar(&f.properties, "property", nil, "aggregate metadata as key=value; repeatable")
	return cmd
}

func runAggregateCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *aggregateCreateFlags, w io.Writer) error {
	agg, err := aggregates.Create(ctx, client, aggregates.CreateOpts{Name: name, AvailabilityZone: f.zone}).Extract()
	if err != nil {
		return fmt.Errorf("creating aggregate %q: %w", name, err)
	}
	// Metadata (properties) is not part of the create body; apply it as a
	// follow-up set_metadata action, then re-fetch so the shown table reflects it.
	if len(f.properties) > 0 {
		meta, err := parseKeyValStrings(f.properties)
		if err != nil {
			return err
		}
		body := make(map[string]any, len(meta))
		for k, v := range meta {
			body[k] = v
		}
		if _, err := aggregates.SetMetadata(ctx, client, agg.ID, aggregates.SetMetadataOpts{Metadata: body}).Extract(); err != nil {
			return fmt.Errorf("setting properties on aggregate %q: %w", name, err)
		}
		agg, err = aggregates.Get(ctx, client, agg.ID).Extract()
		if err != nil {
			return fmt.Errorf("showing aggregate %q: %w", name, err)
		}
	}
	fields, values := aggregateShowFields(agg)
	return o.WriteSingle(w, fields, values)
}

func newAggregateDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <aggregate> [<aggregate> ...]",
		Short: "Delete one or more host aggregates",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runAggregateDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	var errs []error
	for _, ref := range refs {
		id, err := resolveAggregateID(ctx, client, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := aggregates.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting aggregate %q: %w", ref, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted aggregate %s\n", ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type aggregateSetFlags struct {
	name       string
	zone       string
	properties []string
}

func newAggregateSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &aggregateSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <aggregate>",
		Short: "Set host aggregate properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateSet(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name for the aggregate")
	fl.StringVar(&f.zone, "zone", "", "new availability zone for the aggregate")
	fl.StringArrayVar(&f.properties, "property", nil, "metadata to set as key=value; repeatable")
	return cmd
}

func runAggregateSet(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *aggregateSetFlags, _ io.Writer) error {
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	// name / availability_zone are attributes updated via PUT; --property is a
	// separate set_metadata action.
	if f.name != "" || f.zone != "" {
		if _, err := aggregates.Update(ctx, client, id, aggregates.UpdateOpts{Name: f.name, AvailabilityZone: f.zone}).Extract(); err != nil {
			return fmt.Errorf("updating aggregate %q: %w", ref, err)
		}
	}
	if len(f.properties) > 0 {
		meta, err := parseKeyValStrings(f.properties)
		if err != nil {
			return err
		}
		body := make(map[string]any, len(meta))
		for k, v := range meta {
			body[k] = v
		}
		if _, err := aggregates.SetMetadata(ctx, client, id, aggregates.SetMetadataOpts{Metadata: body}).Extract(); err != nil {
			return fmt.Errorf("setting properties on aggregate %q: %w", ref, err)
		}
	}
	return nil
}

func newAggregateUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var properties []string
	cmd := &cobra.Command{
		Use:   "unset <aggregate>",
		Short: "Unset host aggregate properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateUnset(ctx, client, args[0], properties, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&properties, "property", nil, "metadata key to remove; repeatable")
	return cmd
}

func runAggregateUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, properties []string, _ io.Writer) error {
	if len(properties) == 0 {
		return nil
	}
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	// nova removes a metadata key when set_metadata carries it with a null value.
	keys := append([]string(nil), properties...)
	sort.Strings(keys)
	body := make(map[string]any, len(keys))
	for _, k := range keys {
		body[k] = nil
	}
	if _, err := aggregates.SetMetadata(ctx, client, id, aggregates.SetMetadataOpts{Metadata: body}).Extract(); err != nil {
		return fmt.Errorf("removing properties from aggregate %q: %w", ref, err)
	}
	return nil
}

func newAggregateAddHostCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host <aggregate> <host>",
		Short: "Add a host to a host aggregate",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateAddHost(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runAggregateAddHost(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, host string, w io.Writer) error {
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	agg, err := aggregates.AddHost(ctx, client, id, aggregates.AddHostOpts{Host: host}).Extract()
	if err != nil {
		return fmt.Errorf("adding host %q to aggregate %q: %w", host, ref, err)
	}
	fields, values := aggregateShowFields(agg)
	return o.WriteSingle(w, fields, values)
}

func newAggregateRemoveHostCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host <aggregate> <host>",
		Short: "Remove a host from a host aggregate",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAggregateRemoveHost(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runAggregateRemoveHost(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, host string, w io.Writer) error {
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	agg, err := aggregates.RemoveHost(ctx, client, id, aggregates.RemoveHostOpts{Host: host}).Extract()
	if err != nil {
		return fmt.Errorf("removing host %q from aggregate %q: %w", host, ref, err)
	}
	fields, values := aggregateShowFields(agg)
	return o.WriteSingle(w, fields, values)
}
