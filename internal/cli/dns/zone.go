package dns

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newZoneCommand builds "dns zone ..." (exposed as the top-level "zone" noun).
func newZoneCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "zone",
		Short: "Manage DNS zones (designate)",
	}
	cmd.AddCommand(newZoneListCommand(a, o))
	cmd.AddCommand(newZoneShowCommand(a, o))
	cmd.AddCommand(newZoneCreateCommand(a, o))
	cmd.AddCommand(newZoneDeleteCommand(a, o))
	cmd.AddCommand(newZoneSetCommand(a, o))
	return cmd
}

// zoneShowFields is the curated Field/Value view for a single zone, matching the
// most operationally useful attributes shown by `openstack zone show`.
func zoneShowFields(z *zones.Zone) ([]string, []any) {
	fields := []string{
		"id", "name", "type", "email", "ttl", "serial", "status", "action",
		"description", "masters", "pool_id", "project_id", "version",
		"created_at", "updated_at", "transferred_at",
	}
	values := []any{
		z.ID, z.Name, z.Type, z.Email, z.TTL, z.Serial, z.Status, z.Action,
		z.Description, z.Masters, z.PoolID, z.ProjectID, z.Version,
		z.CreatedAt, z.UpdatedAt, z.TransferredAt,
	}
	return fields, values
}

// zoneListFlags holds the filters accepted by "zone list".
//
// Flag names follow upstream OSC (`openstack zone list`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream OSC semantics.
type zoneListFlags struct {
	name   string
	email  string
	typ    string
	ttl    int
	status string
	limit  int
	marker string
}

func newZoneListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &zoneListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DNS zones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by zone name")
	fl.StringVar(&f.email, "email", "", "filter by zone email")
	fl.StringVar(&f.typ, "type", "", "filter by zone type (PRIMARY/SECONDARY)")
	fl.IntVar(&f.ttl, "ttl", 0, "filter by TTL")
	fl.StringVar(&f.status, "status", "", "filter by status")
	fl.IntVar(&f.limit, "limit", 0, "page size for the API request (default 1000; all pages are still fetched)")
	fl.StringVar(&f.marker, "marker", "", "ID of the last zone from the previous page")
	return cmd
}

func runZoneList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *zoneListFlags, w io.Writer) error {
	opts := zones.ListOpts{
		Name:   f.name,
		Email:  f.email,
		Type:   f.typ,
		TTL:    f.ttl,
		Status: f.status,
		Limit:  dnsPageSize(f.limit),
		Marker: f.marker,
	}
	pages, err := zones.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing dns zones: %w", err)
	}
	all, err := zones.ExtractZones(pages)
	if err != nil {
		return fmt.Errorf("parsing dns zone list: %w", err)
	}
	// Limit is only the page size to designate; enforce it as a hard result cap.
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
	}
	return o.WriteList(w, zoneListTable(all))
}

func zoneListTable(list []zones.Zone) output.Table {
	t := output.Table{
		Columns: []string{"ID", "Name", "Type", "Email", "TTL", "Serial", "Status", "Action"},
		Rows:    make([][]any, 0, len(list)),
	}
	for _, z := range list {
		t.Rows = append(t.Rows, []any{z.ID, z.Name, z.Type, z.Email, z.TTL, z.Serial, z.Status, z.Action})
	}
	return t
}

func newZoneShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <zone>",
		Short: "Show details of a DNS zone",
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
			return runZoneShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runZoneShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveZoneID(ctx, client, ref)
	if err != nil {
		return err
	}
	z, err := zones.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting dns zone %s: %w", ref, err)
	}
	fields, values := zoneShowFields(z)
	return o.WriteSingle(w, fields, values)
}

// zoneCreateFlags holds the attributes accepted by "zone create".
//
// Flag names follow upstream OSC (`openstack zone create`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream OSC semantics.
type zoneCreateFlags struct {
	email       string
	ttl         int
	description string
	typ         string
	masters     []string
}

func newZoneCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &zoneCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new DNS zone",
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
			return runZoneCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.email, "email", "", "email address of the zone owner (required for PRIMARY zones)")
	fl.IntVar(&f.ttl, "ttl", 0, "time to live (seconds) for the zone")
	fl.StringVar(&f.description, "description", "", "description of the zone")
	fl.StringVar(&f.typ, "type", "PRIMARY", "zone type: PRIMARY or SECONDARY")
	fl.StringArrayVar(&f.masters, "master", nil, "master nameserver for a SECONDARY zone; repeat for multiple")
	// --email is required for PRIMARY zones only and must be omitted for
	// SECONDARY zones (which require --master instead), so the requirement is
	// enforced conditionally in runZoneCreate rather than via MarkFlagRequired.
	return cmd
}

// zoneType normalizes a --type flag value to its canonical upper-case form,
// treating an empty value as PRIMARY (designate's default).
func zoneType(typ string) string {
	t := strings.ToUpper(strings.TrimSpace(typ))
	if t == "" {
		return "PRIMARY"
	}
	return t
}

// validateZoneCreate enforces designate's PRIMARY/SECONDARY invariants: PRIMARY
// zones require --email and reject --master; SECONDARY zones require at least one
// --master and must not carry --email.
func validateZoneCreate(f *zoneCreateFlags) error {
	switch zoneType(f.typ) {
	case "PRIMARY":
		if f.email == "" {
			return fmt.Errorf("--email is required for PRIMARY zones")
		}
		if len(f.masters) > 0 {
			return fmt.Errorf("--master is only valid for SECONDARY zones")
		}
	case "SECONDARY":
		if f.email != "" {
			return fmt.Errorf("--email must not be set for SECONDARY zones")
		}
		if len(f.masters) == 0 {
			return fmt.Errorf("at least one --master is required for SECONDARY zones")
		}
	default:
		return fmt.Errorf("invalid --type %q: must be PRIMARY or SECONDARY", f.typ)
	}
	return nil
}

func runZoneCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *zoneCreateFlags, w io.Writer) error {
	if err := validateZoneCreate(f); err != nil {
		return err
	}
	opts := zones.CreateOpts{
		Name:        withTrailingDot(name),
		Email:       f.email,
		TTL:         f.ttl,
		Description: f.description,
		Type:        zoneType(f.typ),
		Masters:     f.masters,
	}
	z, err := zones.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating dns zone: %w", err)
	}
	fields, values := zoneShowFields(z)
	return o.WriteSingle(w, fields, values)
}

func newZoneDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <zone> [<zone> ...]",
		Short: "Delete DNS zone(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runZoneDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	var errs []error
	for _, ref := range refs {
		id, err := resolveZoneID(ctx, client, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := zones.Delete(ctx, client, id).Extract(); err != nil {
			errs = append(errs, fmt.Errorf("deleting dns zone %s: %w", ref, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted zone %s\n", ref); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// zoneSetFlags holds the mutable attributes accepted by "zone set".
//
// Flag names follow upstream OSC (`openstack zone set`). UNVERIFIED against the
// KeyStack reference (docs.keystack.ru returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.
type zoneSetFlags struct {
	email       string
	ttl         int
	description string
}

func newZoneSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &zoneSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <zone>",
		Short: "Update a DNS zone",
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
			emailSet := cmd.Flags().Changed("email")
			ttlSet := cmd.Flags().Changed("ttl")
			descSet := cmd.Flags().Changed("description")
			return runZoneSet(ctx, client, o, args[0], f, emailSet, ttlSet, descSet, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.email, "email", "", "set the zone email")
	fl.IntVar(&f.ttl, "ttl", 0, "set the zone TTL (seconds)")
	fl.StringVar(&f.description, "description", "", "set the zone description")
	return cmd
}

// zoneUpdateBody is a raw designate zone-update builder. gophercloud's
// zones.UpdateOpts marshals TTL only when it is > 0 (its ToZoneUpdateMap guards
// on `opts.TTL > 0`), so it can never send `ttl: 0`. Building the request map
// directly lets `zone set --ttl 0` reach the API.
type zoneUpdateBody map[string]any

func (b zoneUpdateBody) ToZoneUpdateMap() (map[string]any, error) { return b, nil }

func runZoneSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *zoneSetFlags, emailSet, ttlSet, descSet bool, w io.Writer) error {
	if !emailSet && !ttlSet && !descSet {
		return fmt.Errorf("zone set requires at least one of --email, --ttl or --description")
	}
	id, err := resolveZoneID(ctx, client, ref)
	if err != nil {
		return err
	}
	body := zoneUpdateBody{}
	if emailSet {
		body["email"] = f.email
	}
	if ttlSet {
		body["ttl"] = f.ttl
	}
	if descSet {
		body["description"] = f.description
	}
	z, err := zones.Update(ctx, client, id, body).Extract()
	if err != nil {
		return fmt.Errorf("updating dns zone %s: %w", ref, err)
	}
	fields, values := zoneShowFields(z)
	return o.WriteSingle(w, fields, values)
}
