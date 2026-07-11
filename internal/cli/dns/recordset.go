package dns

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newRecordSetCommand builds "dns recordset ..." (exposed as the top-level
// "recordset" noun). Recordsets are nested under a zone in the designate API,
// so every verb takes a <zone> reference as its first positional argument.
func newRecordSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recordset",
		Short: "Manage DNS recordsets (designate)",
	}
	cmd.AddCommand(newRecordSetListCommand(a, o))
	cmd.AddCommand(newRecordSetShowCommand(a, o))
	cmd.AddCommand(newRecordSetCreateCommand(a, o))
	cmd.AddCommand(newRecordSetDeleteCommand(a, o))
	cmd.AddCommand(newRecordSetSetCommand(a, o))
	return cmd
}

// recordSetShowFields is the curated Field/Value view for a single recordset,
// matching `openstack recordset show`.
func recordSetShowFields(rs *recordsets.RecordSet) ([]string, []any) {
	fields := []string{
		"id", "name", "type", "records", "ttl", "status", "action",
		"description", "zone_id", "zone_name", "project_id", "version",
		"created_at", "updated_at",
	}
	values := []any{
		rs.ID, rs.Name, rs.Type, rs.Records, rs.TTL, rs.Status, rs.Action,
		rs.Description, rs.ZoneID, rs.ZoneName, rs.ProjectID, rs.Version,
		rs.CreatedAt, rs.UpdatedAt,
	}
	return fields, values
}

// recordSetListFlags holds the filters accepted by "recordset list".
//
// Flag names follow upstream OSC (`openstack recordset list`). UNVERIFIED
// against the KeyStack reference (docs.keystack.ru returned HTTP 403 at
// implementation time); falls back to upstream OSC semantics.
type recordSetListFlags struct {
	name   string
	typ    string
	data   string
	ttl    int
	status string
	limit  int
	marker string
}

func newRecordSetListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &recordSetListFlags{}
	cmd := &cobra.Command{
		Use:   "list <zone>",
		Short: "List recordsets in a DNS zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runRecordSetList(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by recordset name")
	fl.StringVar(&f.typ, "type", "", "filter by RRTYPE (A/AAAA/CNAME/MX/TXT/...)")
	fl.StringVar(&f.data, "data", "", "filter by record data")
	fl.IntVar(&f.ttl, "ttl", 0, "filter by TTL")
	fl.StringVar(&f.status, "status", "", "filter by status")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of recordsets to return")
	fl.StringVar(&f.marker, "marker", "", "ID of the last recordset from the previous page")
	return cmd
}

func runRecordSetList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, zoneRef string, f *recordSetListFlags, w io.Writer) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	opts := recordsets.ListOpts{
		Name:   f.name,
		Type:   f.typ,
		Data:   f.data,
		TTL:    f.ttl,
		Status: f.status,
		Limit:  f.limit,
		Marker: f.marker,
	}
	pages, err := recordsets.ListByZone(client, zoneID, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing recordsets in zone %s: %w", zoneRef, err)
	}
	all, err := recordsets.ExtractRecordSets(pages)
	if err != nil {
		return fmt.Errorf("parsing recordset list: %w", err)
	}
	return o.WriteList(w, recordSetListTable(all))
}

func recordSetListTable(list []recordsets.RecordSet) output.Table {
	t := output.Table{
		Columns: []string{"ID", "Name", "Type", "Records", "TTL", "Status", "Action"},
		Rows:    make([][]any, 0, len(list)),
	}
	for _, rs := range list {
		t.Rows = append(t.Rows, []any{rs.ID, rs.Name, rs.Type, rs.Records, rs.TTL, rs.Status, rs.Action})
	}
	return t
}

func newRecordSetShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <zone> <recordset>",
		Short: "Show details of a recordset",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runRecordSetShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runRecordSetShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, zoneRef, rsRef string, w io.Writer) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	rsID, err := resolveRecordSetID(ctx, client, zoneID, rsRef)
	if err != nil {
		return err
	}
	rs, err := recordsets.Get(ctx, client, zoneID, rsID).Extract()
	if err != nil {
		return fmt.Errorf("getting recordset %s: %w", rsRef, err)
	}
	fields, values := recordSetShowFields(rs)
	return o.WriteSingle(w, fields, values)
}

// recordSetCreateFlags holds the attributes accepted by "recordset create".
//
// Flag names follow upstream OSC (`openstack recordset create`). UNVERIFIED
// against the KeyStack reference (docs.keystack.ru returned HTTP 403 at
// implementation time); falls back to upstream OSC semantics.
type recordSetCreateFlags struct {
	typ         string
	records     []string
	ttl         int
	description string
}

func newRecordSetCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &recordSetCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <zone> <name>",
		Short: "Create a recordset in a DNS zone",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runRecordSetCreate(ctx, client, o, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.typ, "type", "", "RRTYPE of the recordset (A/AAAA/CNAME/MX/TXT/...)")
	fl.StringArrayVar(&f.records, "record", nil, "record data; repeat for multiple records")
	fl.IntVar(&f.ttl, "ttl", 0, "time to live (seconds) for the recordset")
	fl.StringVar(&f.description, "description", "", "description of the recordset")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func runRecordSetCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, zoneRef, name string, f *recordSetCreateFlags, w io.Writer) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	opts := recordsets.CreateOpts{
		Name:        withTrailingDot(name),
		Type:        f.typ,
		Records:     f.records,
		TTL:         f.ttl,
		Description: f.description,
	}
	rs, err := recordsets.Create(ctx, client, zoneID, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating recordset in zone %s: %w", zoneRef, err)
	}
	fields, values := recordSetShowFields(rs)
	return o.WriteSingle(w, fields, values)
}

func newRecordSetDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <zone> <recordset> [<recordset> ...]",
		Short: "Delete recordset(s) from a DNS zone",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runRecordSetDelete(ctx, client, args[0], args[1:], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runRecordSetDelete(ctx context.Context, client *gophercloud.ServiceClient, zoneRef string, rsRefs []string, w io.Writer) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	for _, ref := range rsRefs {
		rsID, err := resolveRecordSetID(ctx, client, zoneID, ref)
		if err != nil {
			return err
		}
		if err := recordsets.Delete(ctx, client, zoneID, rsID).ExtractErr(); err != nil {
			return fmt.Errorf("deleting recordset %s: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted recordset %s\n", ref); err != nil {
			return err
		}
	}
	return nil
}

// recordSetSetFlags holds the mutable attributes accepted by "recordset set".
//
// Flag names follow upstream OSC (`openstack recordset set`). UNVERIFIED against
// the KeyStack reference (docs.keystack.ru returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.
type recordSetSetFlags struct {
	records     []string
	ttl         int
	description string
}

func newRecordSetSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &recordSetSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <zone> <recordset>",
		Short: "Update a recordset",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			recordsSet := cmd.Flags().Changed("record")
			ttlSet := cmd.Flags().Changed("ttl")
			descSet := cmd.Flags().Changed("description")
			return runRecordSetSet(ctx, client, o, args[0], args[1], f, recordsSet, ttlSet, descSet, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.records, "record", nil, "replace record data; repeat for multiple records")
	fl.IntVar(&f.ttl, "ttl", 0, "set the recordset TTL (seconds)")
	fl.StringVar(&f.description, "description", "", "set the recordset description")
	return cmd
}

func runRecordSetSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, zoneRef, rsRef string, f *recordSetSetFlags, recordsSet, ttlSet, descSet bool, w io.Writer) error {
	if !recordsSet && !ttlSet && !descSet {
		return fmt.Errorf("recordset set requires at least one of --record, --ttl or --description")
	}
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	rsID, err := resolveRecordSetID(ctx, client, zoneID, rsRef)
	if err != nil {
		return err
	}
	var opts recordsets.UpdateOpts
	if recordsSet {
		opts.Records = f.records
	}
	if ttlSet {
		ttl := f.ttl
		opts.TTL = &ttl
	}
	if descSet {
		d := f.description
		opts.Description = &d
	}
	rs, err := recordsets.Update(ctx, client, zoneID, rsID, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating recordset %s: %w", rsRef, err)
	}
	fields, values := recordSetShowFields(rs)
	return o.WriteSingle(w, fields, values)
}
