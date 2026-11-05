package dns

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	dnsapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
)

// Designate's remaining read surface plus floating-IP PTR records. None has a
// gophercloud package, so all of them go through the raw helpers in raw.go:
//
//	dns pool        GET /v2/pools, GET /v2/pools/{id}
//	ptr record      GET /v2/reverse/floatingips, GET|PATCH /v2/reverse/floatingips/{region}:{id}
//	dns service     GET /v2/service_statuses, GET /v2/service_statuses/{id}
//
// Command and flag names follow upstream python-designateclient 7.0.0
// (`openstack ptr record list|set|show|unset`, `openstack dns service list|show`)
// except for `dns pool`, which has no upstream command at all — see the note on
// newDNSPoolCommand. The KeyStack command reference at https://docs.keystack.ru/
// was not reachable at implementation time (HTTP 403), so these are UNVERIFIED
// against KeyStack and fall back to upstream semantics.

// --- dns pool --------------------------------------------------------------

func poolFields(p *dnsapi.Pool) ([]string, []any) {
	return []string{"id", "name", "description", "project_id", "ns_records", "created_at", "updated_at"},
		[]any{p.ID, p.Name, p.Description, p.ProjectID, nsRecords(p), dnsTimeString(p.CreatedAt), dnsTimeString(p.UpdatedAt)}
}

// nsRecords renders a pool's nameservers as "priority:hostname" lines.
func nsRecords(p *dnsapi.Pool) string {
	if len(p.NS) == 0 {
		return ""
	}
	lines := make([]string, 0, len(p.NS))
	for _, ns := range p.NS {
		lines = append(lines, fmt.Sprintf("%d:%s", ns.Priority, ns.Hostname))
	}
	return strings.Join(lines, "\n")
}

// newDNSPoolCommand builds "dns pool list|show".
//
// This one is koc-native: python-designateclient 7.0.0 ships a PoolController in
// its SDK but registers no `openstack` command for it, so there is no upstream
// spelling to mirror. `dns pool` follows the service-prefixed shape designate uses
// for its other deployment-level nouns (`dns quota`, `dns service`). Reads only —
// pool *writes* are a designate-manage / config-file operation on the servers, not
// an API one.
func newDNSPoolCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pool",
		Short: "Inspect the nameserver pools zones are served from (read-only)",
	}
	cmd.AddCommand(
		newDNSPoolListCommand(a, o),
		newDNSPoolShowCommand(a, o),
	)
	return cmd
}

func newDNSPoolListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	var limit int
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List nameserver pools",
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
			return runDNSPoolList(ctx, client, o, name, limit, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "filter by pool name")
	fl.IntVar(&limit, "limit", 0, "maximum number of pools to return")
	common.bind(cmd)
	return cmd
}

func runDNSPoolList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, limit int, common *commonOptions, w io.Writer,
) error {
	all, err := dnsapi.ListPools(ctx, withCommonHeaders(client, common), name, limit)
	if err != nil {
		return err
	}
	t := output.Table{
		Columns: []string{"ID", "Name", "Description", "NS Records"},
		Rows:    make([][]any, 0, len(all)),
	}
	for i := range all {
		p := &all[i]
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.Description, nsRecords(p)})
	}
	return o.WriteList(w, t)
}

// resolvePoolID accepts a pool name as well as an ID; the default pool's name
// ("default") is what operators actually have in hand.
func resolvePoolID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return lookup("DNS pool name %q is ambiguous: %d matches, use the ID", ref, func() (string, error) {
		return dnsapi.FindPoolID(ctx, client, ref)
	})
}

func newDNSPoolShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <pool>",
		Short: "Show a nameserver pool",
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
			return runDNSPoolShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runDNSPoolShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, common *commonOptions, w io.Writer,
) error {
	client = withCommonHeaders(client, common)
	id, err := resolvePoolID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := dnsapi.GetPool(ctx, client, id)
	if err != nil {
		return relabel(err, "showing DNS pool %q", ref)
	}
	fields, values := poolFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- ptr record ------------------------------------------------------------

func ptrRecordFields(r *dnsapi.PTRRecord) ([]string, []any) {
	ttl := any("")
	if r.TTL != nil {
		ttl = *r.TTL
	}
	return []string{"id", "ptrdname", "description", "address", "ttl", "status", "action"},
		[]any{r.ID, r.PTRDName, r.Description, r.Address, ttl, r.Status, r.Action}
}

func newPTRRecordCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ptr",
		Short: "Manage reverse-DNS (PTR) records for floating IPs",
	}
	record := &cobra.Command{
		Use:   "record",
		Short: "Manage reverse-DNS (PTR) records for floating IPs",
	}
	record.AddCommand(
		newPTRRecordListCommand(a, o),
		newPTRRecordShowCommand(a, o),
		newPTRRecordSetCommand(a, o),
		newPTRRecordUnsetCommand(a, o),
	)
	// Upstream's noun is the two words "ptr record", so model it as a nested parent
	// the way the other two-word nouns are.
	cmd.AddCommand(record)
	return cmd
}

func newPTRRecordListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var limit int
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List floating-IP PTR records",
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
			return runPTRRecordList(ctx, client, o, limit, common, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of records to return")
	common.bind(cmd)
	return cmd
}

func runPTRRecordList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	limit int, common *commonOptions, w io.Writer,
) error {
	all, err := dnsapi.ListPTRRecords(ctx, withCommonHeaders(client, common), limit)
	if err != nil {
		return err
	}
	t := output.Table{
		Columns: []string{"ID", "PTRD Name", "Description", "TTL"},
		Rows:    make([][]any, 0, len(all)),
	}
	for i := range all {
		r := &all[i]
		ttl := any("")
		if r.TTL != nil {
			ttl = *r.TTL
		}
		t.Rows = append(t.Rows, []any{r.ID, r.PTRDName, r.Description, ttl})
	}
	return o.WriteList(w, t)
}

// validatePTRRecordID rejects a bare UUID early: designate addresses a reverse
// record as "<region>:<floating-ip-id>", and without the region prefix the API
// answers a bare 404 that gives no hint what was wrong.
func validatePTRRecordID(id string) error {
	if region, _, found := strings.Cut(id, ":"); !found || region == "" {
		return fmt.Errorf("floating IP %q must be given as <region>:<floating-ip-id>", id)
	}
	return nil
}

func newPTRRecordShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <region>:<floating-ip-id>",
		Short: "Show a floating IP's PTR record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := validatePTRRecordID(args[0]); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runPTRRecordShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runPTRRecordShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, common *commonOptions, w io.Writer,
) error {
	r, err := dnsapi.GetPTRRecord(ctx, withCommonHeaders(client, common), id)
	if err != nil {
		return err
	}
	fields, values := ptrRecordFields(r)
	return o.WriteSingle(w, fields, values)
}

type ptrRecordSetFlags struct {
	description   string
	noDescription bool
	ttl           int
	noTTL         bool
	common        *commonOptions
}

func newPTRRecordSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &ptrRecordSetFlags{common: &commonOptions{}}
	cmd := &cobra.Command{
		Use:   "set <region>:<floating-ip-id> <ptrdname>",
		Short: "Set a floating IP's PTR record",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := validatePTRRecordID(args[0]); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runPTRRecordSet(ctx, client, o, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", flagDescription)
	fl.BoolVar(&f.noDescription, flagNoDescription, false, "clear the description")
	fl.IntVar(&f.ttl, "ttl", 0, "TTL in seconds")
	fl.BoolVar(&f.noTTL, "no-ttl", false, "clear the TTL, falling back to the pool default")
	cmd.MarkFlagsMutuallyExclusive(flagDescription, flagNoDescription)
	cmd.MarkFlagsMutuallyExclusive("ttl", "no-ttl")
	f.common.bind(cmd)
	return cmd
}

// runPTRRecordSet PATCHes the reverse record. ptrdname is mandatory — designate
// treats the PATCH as "this is the record now", and a nil ptrdname is how the
// record is *removed* (see runPTRRecordUnset), so it is never omitted here.
func runPTRRecordSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, ptrdname string, f *ptrRecordSetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(ptrRecordColumns...); err != nil {
		return err
	}
	body := map[string]any{"ptrdname": ptrdname}
	switch {
	case f.noDescription:
		body["description"] = nil
	case f.description != "":
		body["description"] = f.description
	}
	switch {
	case f.noTTL:
		body["ttl"] = nil
	case f.ttl != 0:
		body["ttl"] = f.ttl
	}
	r, err := dnsapi.SetPTRRecord(ctx, withCommonHeaders(client, f.common), id, body)
	if err != nil {
		return err
	}
	fields, values := ptrRecordFields(r)
	return o.WriteSingle(w, fields, values)
}

func newPTRRecordUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "unset <region>:<floating-ip-id>",
		Short: "Remove a floating IP's PTR record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := validatePTRRecordID(args[0]); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runPTRRecordUnset(ctx, client, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

// runPTRRecordUnset removes the record by PATCHing a null ptrdname — designate has
// no DELETE for a reverse record, since the floating IP itself is not being
// deleted.
func runPTRRecordUnset(ctx context.Context, client *gophercloud.ServiceClient,
	id string, common *commonOptions, w io.Writer,
) error {
	if err := dnsapi.UnsetPTRRecord(ctx, withCommonHeaders(client, common), id); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Unset PTR record for floating IP %s\n", id); err != nil {
		return err
	}
	return nil
}

// --- dns service -----------------------------------------------------------

// mapOrDash renders designate's stats/capabilities objects: "-" when empty, and
// otherwise one "key=value" per line, sorted by key so the output is stable.
func mapOrDash(values map[string]any) string {
	if len(values) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%v", k, values[k]))
	}
	return strings.Join(lines, "\n")
}

func serviceStatusFields(s *dnsapi.ServiceStatus) ([]string, []any) {
	return []string{"id", "hostname", "service_name", "status", "stats", "capabilities", "heartbeated_at"},
		[]any{s.ID, s.Hostname, s.ServiceName, s.Status,
			mapOrDash(s.Stats), mapOrDash(s.Capabilities), dnsTimeString(s.HeartbeatedAt)}
}

func newDNSServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Show the health of the designate services (read-only)",
	}
	cmd.AddCommand(
		newDNSServiceListCommand(a, o),
		newDNSServiceShowCommand(a, o),
	)
	return cmd
}

type dnsServiceListFlags struct {
	hostname    string
	serviceName string
	status      string
	limit       int
}

func newDNSServiceListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &dnsServiceListFlags{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List designate service statuses",
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
			return runDNSServiceList(ctx, client, o, f, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.hostname, "hostname", "", "filter by hostname")
	fl.StringVar(&f.serviceName, "service-name", "", "filter by service name, e.g. central or worker")
	fl.StringVar(&f.status, "status", "", "filter by status, e.g. UP or DOWN")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of statuses to return")
	// Upstream spells this one with an underscore (`--service_name`) where every
	// other designate flag uses dashes. Accept it so existing scripts keep working,
	// but keep it out of --help so the dashed form is the one people learn.
	fl.StringVar(&f.serviceName, "service_name", "", "alias for --service-name")
	_ = fl.MarkHidden("service_name")
	cmd.MarkFlagsMutuallyExclusive("service-name", "service_name")
	common.bind(cmd)
	return cmd
}

func runDNSServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *dnsServiceListFlags, common *commonOptions, w io.Writer,
) error {
	all, err := dnsapi.ListServiceStatuses(ctx, withCommonHeaders(client, common),
		dnsapi.ServiceStatusListOpts{Hostname: f.hostname, ServiceName: f.serviceName, Status: f.status}, f.limit)
	if err != nil {
		return err
	}
	t := output.Table{
		Columns: []string{"ID", "Hostname", "Service Name", "Status", "Stats", "Capabilities"},
		Rows:    make([][]any, 0, len(all)),
	}
	for i := range all {
		s := &all[i]
		t.Rows = append(t.Rows, []any{s.ID, s.Hostname, s.ServiceName, s.Status,
			mapOrDash(s.Stats), mapOrDash(s.Capabilities)})
	}
	return o.WriteList(w, t)
}

func newDNSServiceShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <service-status-id>",
		Short: "Show one designate service status",
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
			return runDNSServiceShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runDNSServiceShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, common *commonOptions, w io.Writer,
) error {
	s, err := dnsapi.GetServiceStatus(ctx, withCommonHeaders(client, common), id)
	if err != nil {
		return err
	}
	fields, values := serviceStatusFields(s)
	return o.WriteSingle(w, fields, values)
}
