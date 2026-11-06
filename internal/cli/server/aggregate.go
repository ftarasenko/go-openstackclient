package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/cli/batchdelete"
	"github.com/ftarasenko/koc/internal/output"
	computeapi "github.com/ftarasenko/koc/pkg/openstack/compute"
	osresolve "github.com/ftarasenko/koc/pkg/openstack/resolve"
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
// and returns the numeric ID the nova API keys on (computeapi.FindAggregateID),
// in this command's wording for no match and several.
func resolveAggregateID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (int, error) {
	id, err := computeapi.FindAggregateID(ctx, client, ref)
	var notFound *osresolve.NotFoundError
	var ambiguous *osresolve.AmbiguousError
	switch {
	case errors.As(err, &notFound):
		return 0, fmt.Errorf("no aggregate found with name %q", ref)
	case errors.As(err, &ambiguous):
		return 0, fmt.Errorf("more than one aggregate named %q; specify the ID", ref)
	}
	return id, err
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
	all, err := computeapi.ListAggregates(ctx, client)
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
	agg, err := computeapi.GetAggregate(ctx, client, id)
	if err != nil {
		return relabel(err, "showing aggregate %q", ref)
	}
	fields, values := aggregateShowFields(agg)
	return o.WriteSingle(w, fields, values)
}

// aggregateColumns are the fields of a single aggregate.
var aggregateColumns = []string{"ID", "Name", "Availability Zone", "Hosts", "Properties", "UUID", "Created At", "Updated At"}

func aggregateShowFields(agg *aggregates.Aggregate) ([]string, []any) {
	fields := aggregateColumns
	values := []any{
		agg.ID, agg.Name, agg.AvailabilityZone, strings.Join(agg.Hosts, ", "),
		formatAggregateMetadata(agg.Metadata), agg.UUID,
		agg.CreatedAt, agg.UpdatedAt,
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
	if err := o.CheckColumns(aggregateColumns...); err != nil {
		return err
	}
	// --property is parsed before the create, so a malformed one leaves no
	// aggregate behind.
	meta, err := parseKeyValStrings(f.properties)
	if err != nil {
		return err
	}
	agg, err := computeapi.CreateAggregate(ctx, client, aggregates.CreateOpts{Name: name, AvailabilityZone: f.zone})
	if err != nil {
		return err
	}
	// Metadata (properties) is not part of the create body; apply it as a
	// follow-up set_metadata action, then re-fetch so the shown table reflects it.
	if len(meta) > 0 {
		body := make(map[string]any, len(meta))
		for k, v := range meta {
			body[k] = v
		}
		if _, err := computeapi.SetAggregateMetadata(ctx, client, agg.ID, body); err != nil {
			return relabel(err, "aggregate %q was created (id %d) but setting its properties failed", name, agg.ID)
		}
		agg, err = computeapi.GetAggregate(ctx, client, agg.ID)
		if err != nil {
			return relabel(err, "showing aggregate %q", name)
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
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveAggregateID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := computeapi.DeleteAggregate(ctx, client, id); err != nil {
			return relabel(err, "deleting aggregate %q", ref)
		}
		if _, err := fmt.Fprintf(w, "Deleted aggregate %s\n", ref); err != nil {
			return err
		}
		return nil
	})
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
		if _, err := computeapi.UpdateAggregate(ctx, client, id, aggregates.UpdateOpts{Name: f.name, AvailabilityZone: f.zone}); err != nil {
			return relabel(err, "updating aggregate %q", ref)
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
		if _, err := computeapi.SetAggregateMetadata(ctx, client, id, body); err != nil {
			return relabel(err, "setting properties on aggregate %q", ref)
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
	if _, err := computeapi.SetAggregateMetadata(ctx, client, id, body); err != nil {
		return relabel(err, "removing properties from aggregate %q", ref)
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
	if err := o.CheckColumns(aggregateColumns...); err != nil {
		return err
	}
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	agg, err := computeapi.AddAggregateHost(ctx, client, id, host)
	if err != nil {
		return relabel(err, "adding host %q to aggregate %q", host, ref)
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
	if err := o.CheckColumns(aggregateColumns...); err != nil {
		return err
	}
	id, err := resolveAggregateID(ctx, client, ref)
	if err != nil {
		return err
	}
	agg, err := computeapi.RemoveAggregateHost(ctx, client, id, host)
	if err != nil {
		return relabel(err, "removing host %q from aggregate %q", host, ref)
	}
	fields, values := aggregateShowFields(agg)
	return o.WriteSingle(w, fields, values)
}
