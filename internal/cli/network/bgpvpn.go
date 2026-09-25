package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "bgpvpn" and its network/router/port associations — the networking-bgpvpn
// plugin (upstream openstackclient/network/v2/bgpvpn/*.py). The plugin is only
// reachable where neutron loads it: the bgpvpn extension for the VPNs and their
// network and router associations, plus bgpvpn-routes-control for port
// associations and a router association's advertise_extra_routes.
//
// Flag names follow upstream OSC (UNVERIFIED against KeyStack, see the package
// comment).

// Flag names shared by the bgpvpn verbs. Batch-specific names so they cannot
// collide with another noun's constants in this package.
const (
	flagBGPVPNRouteTarget  = "route-target"
	flagBGPVPNImportTarget = "import-target"
	flagBGPVPNExportTarget = "export-target"
	flagBGPVPNRouteDistg   = "route-distinguisher"
	flagBGPVPNVNI          = "vni"
	flagBGPVPNLocalPref    = "local-pref"
	flagBGPVPNProperty     = "property"
	flagBGPVPNLong         = "long"
	bgpvpnPropertyHelp     = "filter on key=value (repeatable)"
	bgpvpnLongHelp         = "list additional fields in output"
	bgpvpnVNIHelp          = "VXLAN network identifier used when VXLAN encapsulation is in use"
	bgpvpnLocalPrefHelp    = "default BGP LOCAL_PREF for route advertisements towards this BGP VPN"
	bgpvpnRefArg           = "<bgpvpn>"
	bgpvpnTypeL2           = "l2"
	bgpvpnTypeL3           = "l3"
)

func newBGPVPNCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	cmd := &cobra.Command{
		Use: "bgpvpn",
		Short: "Manage BGP VPNs and their network, router and port associations " +
			"(needs the neutron bgpvpn extension; port associations also bgpvpn-routes-control)",
	}
	cmd.AddCommand(
		newBGPVPNCreateCommand(a, o),
		newBGPVPNDeleteCommand(a),
		newBGPVPNListCommand(a, o),
		newBGPVPNSetCommand(a, o, false),
		newBGPVPNSetCommand(a, o, true),
		newBGPVPNShowCommand(a, o),
		newBGPVPNNetworkAssocCommand(a, o),
		newBGPVPNRouterAssocCommand(a, o),
		newBGPVPNPortAssocCommand(a, o),
	)
	return []*cobra.Command{cmd}
}

// bgpvpnQuery is a raw list query for the bgpvpn collections. It satisfies
// every list builder of the bgpvpns package: upstream's --property passes any
// key=value straight through as a filter, which none of the typed ListOpts can
// express (and bgpvpns.ListOpts has no name filter for the resolver either).
type bgpvpnQuery url.Values

func (q bgpvpnQuery) encode() (string, error) {
	return withQueryValues("", nil, url.Values(q))
}

func (q bgpvpnQuery) ToBGPVPNListQuery() (string, error)              { return q.encode() }
func (q bgpvpnQuery) ToNetworkAssociationsListQuery() (string, error) { return q.encode() }
func (q bgpvpnQuery) ToRouterAssociationsListQuery() (string, error)  { return q.encode() }
func (q bgpvpnQuery) ToPortAssociationsListQuery() (string, error)    { return q.encode() }

// parseBGPVPNProperties turns --property key=value specs into a query
// (osc-lib KeyValueAction: split on the first '=', the key may not be empty).
func parseBGPVPNProperties(specs []string) (bgpvpnQuery, error) {
	q := bgpvpnQuery{}
	for _, spec := range specs {
		k, v, err := splitKV(spec)
		if err != nil || k == "" {
			return nil, fmt.Errorf("--property %q: expected key=value", spec)
		}
		// KeyValueAction keeps the last value of a repeated key.
		q[k] = []string{v}
	}
	return q, nil
}

// bgpvpnBody is a raw bgpvpn request body. bgpvpns.UpdateOpts has no vni, and
// both typed opts drop a zero vni/local_pref that upstream sends when given;
// upstream builds the body attribute by attribute, and so does koc.
type bgpvpnBody map[string]any

func (b bgpvpnBody) ToBGPVPNCreateMap() (map[string]any, error) {
	return map[string]any{"bgpvpn": map[string]any(b)}, nil
}

func (b bgpvpnBody) ToBGPVPNUpdateMap() (map[string]any, error) {
	return map[string]any{"bgpvpn": map[string]any(b)}, nil
}

// resolveBGPVPNID resolves a BGP VPN name or ID to its ID.
func resolveBGPVPNID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	id, err := resolveByName(client, "BGP VPN", nameOrID, func(c *gophercloud.ServiceClient) ([]bgpvpns.BGPVPN, error) {
		pages, err := bgpvpns.List(c, bgpvpnQuery{"name": {nameOrID}}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return bgpvpns.ExtractBGPVPNs(pages)
	}, func(b bgpvpns.BGPVPN) string { return b.ID })
	return id, explainMissingService(ctx, client, err, extBGPVPN)
}

// explainBGPVPNRoutesCtl is explainMissingService for the calls that also need
// bgpvpn-routes-control: port associations (a 404 without it) and a router
// association's advertise_extra_routes (a 400 "unrecognized attribute"). A
// cloud without bgpvpn at all is named as such; otherwise, when routes-control
// is absent, the error says so. Any other outcome returns err unchanged.
func explainBGPVPNRoutesCtl(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	var httpErr gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &httpErr) || (httpErr.Actual != http.StatusNotFound && httpErr.Actual != http.StatusBadRequest) {
		return err
	}
	exts, lerr := listNetworkExtensions(ctx, client)
	if lerr != nil {
		return err
	}
	enabled := map[string]bool{}
	for _, e := range exts {
		enabled[e.Alias] = true
	}
	switch {
	case !enabled[extBGPVPN]:
		return explainMissingService(ctx, client, err, extBGPVPN)
	case !enabled[extBGPVPNRoutesCtl]:
		return fmt.Errorf("%w\nthis cloud's neutron does not enable the %s extension; "+
			"BGP VPN port associations and advertise_extra_routes need it", err, extBGPVPNRoutesCtl)
	}
	return err
}

// --- show / list -------------------------------------------------------------

// bgpvpnShowFields renders upstream's show columns (the SDK resource's
// attributes, sorted) plus the association lists neutron also returns.
func bgpvpnShowFields(b *bgpvpns.BGPVPN) ([]string, []any) {
	var localPref any
	if b.LocalPref != nil {
		localPref = *b.LocalPref
	}
	return []string{
		"export_targets", "id", "import_targets", "local_pref", "name", "networks", "ports",
		"project_id", "route_distinguishers", "route_targets", "routers", "type", "vni",
	}, []any{
		b.ExportTargets, b.ID, b.ImportTargets, localPref, b.Name, b.Networks, b.Ports,
		b.ProjectID, b.RouteDistinguishers, b.RouteTargets, b.Routers, b.Type, b.VNI,
	}
}

func newBGPVPNShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show " + bgpvpnRefArg,
		Short: "Show a BGP VPN",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPVPNShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runBGPVPNShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveBGPVPNID(ctx, client, ref)
	if err != nil {
		return err
	}
	b, err := bgpvpns.Get(ctx, client, id).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("showing BGP VPN %s: %w", ref, err), extBGPVPN)
	}
	fields, values := bgpvpnShowFields(b)
	return o.WriteSingle(w, fields, values)
}

type bgpvpnListFlags struct {
	project       string
	projectDomain string
	long          bool
	properties    []string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newBGPVPNListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &bgpvpnListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List BGP VPNs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runBGPVPNList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.project, flagProject, "", "list only BGP VPNs owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.BoolVar(&f.long, flagBGPVPNLong, false, bgpvpnLongHelp)
	fl.StringArrayVar(&f.properties, flagBGPVPNProperty, nil, bgpvpnPropertyHelp)
	return cmd
}

func runBGPVPNList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *bgpvpnListFlags, w io.Writer) error {
	q := bgpvpnQuery{}
	if f.projectID != "" {
		q["project_id"] = []string{f.projectID}
	}
	// Upstream applies --property after the project filter, so it wins.
	props, err := parseBGPVPNProperties(f.properties)
	if err != nil {
		return err
	}
	for k, v := range props {
		q[k] = v
	}
	pages, err := bgpvpns.List(client, q).AllPages(ctx)
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("listing BGP VPNs: %w", err), extBGPVPN)
	}
	all, err := bgpvpns.ExtractBGPVPNs(pages)
	if err != nil {
		return fmt.Errorf("parsing the BGP VPN list: %w", err)
	}
	return o.WriteList(w, bgpvpnListTable(all, f.long))
}

// bgpvpnListTable renders upstream's columns: ID, Name and Type by default;
// --long adds the project, the four target/distinguisher lists, the
// associations, VNI and local pref (in upstream's order).
func bgpvpnListTable(all []bgpvpns.BGPVPN, long bool) output.Table {
	cols := []string{"ID", "Name", "Type"}
	if long {
		cols = []string{
			"ID", "Project", "Name", "Type", "Route Targets", "Import Targets", "Export Targets",
			"Route Distinguishers", "Associated Networks", "Associated Routers", "Associated Ports",
			"VNI", "Local Pref",
		}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, b := range all {
		if !long {
			t.Rows = append(t.Rows, []any{b.ID, b.Name, b.Type})
			continue
		}
		var localPref any
		if b.LocalPref != nil {
			localPref = *b.LocalPref
		}
		t.Rows = append(t.Rows, []any{
			b.ID, b.ProjectID, b.Name, b.Type, b.RouteTargets, b.ImportTargets, b.ExportTargets,
			b.RouteDistinguishers, b.Networks, b.Routers, b.Ports, b.VNI, localPref,
		})
	}
	return t
}

// --- create ------------------------------------------------------------------

type bgpvpnCreateFlags struct {
	name                string
	bgpvpnType          string
	routeTargets        []string
	importTargets       []string
	exportTargets       []string
	routeDistinguishers []string
	vni                 int
	localPref           int
	project             string
	projectDomain       string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newBGPVPNCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &bgpvpnCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a BGP VPN",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runBGPVPNCreate(ctx, client, o, f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "name of the BGP VPN")
	fl.StringVar(&f.bgpvpnType, "type", bgpvpnTypeL3, "BGP VPN type: l3 (IP VPN) or l2 (Ethernet VPN)")
	fl.StringArrayVar(&f.routeTargets, flagBGPVPNRouteTarget, nil, "route target for both import and export (repeatable)")
	fl.StringArrayVar(&f.importTargets, flagBGPVPNImportTarget, nil, "import route target (repeatable)")
	fl.StringArrayVar(&f.exportTargets, flagBGPVPNExportTarget, nil, "export route target (repeatable)")
	fl.StringArrayVar(&f.routeDistinguishers, flagBGPVPNRouteDistg, nil,
		"route distinguisher to pick from when advertising a VPN route (repeatable)")
	fl.IntVar(&f.vni, flagBGPVPNVNI, 0, bgpvpnVNIHelp)
	fl.IntVar(&f.localPref, flagBGPVPNLocalPref, 0, bgpvpnLocalPrefHelp)
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	return cmd
}

func runBGPVPNCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *bgpvpnCreateFlags, flags flagSet, w io.Writer,
) error {
	if f.bgpvpnType != bgpvpnTypeL2 && f.bgpvpnType != bgpvpnTypeL3 {
		return fmt.Errorf("--type must be %s or %s, got %q", bgpvpnTypeL2, bgpvpnTypeL3, f.bgpvpnType)
	}
	body := bgpvpnBody{"type": f.bgpvpnType}
	if flags.Changed("name") {
		body["name"] = f.name
	}
	for key, v := range map[string][]string{
		"route_targets": f.routeTargets, "import_targets": f.importTargets,
		"export_targets": f.exportTargets, "route_distinguishers": f.routeDistinguishers,
	} {
		if v != nil {
			body[key] = v
		}
	}
	if flags.Changed(flagBGPVPNVNI) {
		body["vni"] = f.vni
	}
	if flags.Changed(flagBGPVPNLocalPref) {
		body["local_pref"] = f.localPref
	}
	if f.projectID != "" {
		body["project_id"] = f.projectID
	}
	b, err := bgpvpns.Create(ctx, client, body).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("creating BGP VPN: %w", err), extBGPVPN)
	}
	fields, values := bgpvpnShowFields(b)
	return o.WriteSingle(w, fields, values)
}

// --- set / unset -------------------------------------------------------------

// bgpvpnUpdateFlags backs both set and unset: upstream's _get_common_parser
// gives them the same list flags (adding on set, removing on unset), the
// purge flag of each list (--no-* on set, --all-* on unset), and --vni and
// --local-pref, which even unset sends as a value.
type bgpvpnUpdateFlags struct {
	// unset is the verb the flags were bound for: on unset a list flag removes.
	unset               bool
	name                string
	routeTargets        []string
	importTargets       []string
	exportTargets       []string
	routeDistinguishers []string
	purgeRouteTargets   bool
	purgeImportTargets  bool
	purgeExportTargets  bool
	purgeRDs            bool
	vni                 int
	localPref           int
}

// bgpvpnListAttr is one of the four list attributes a set/unset edits.
type bgpvpnListAttr struct {
	key     string
	given   []string
	purge   bool
	current func(*bgpvpns.BGPVPN) []string
}

func (f *bgpvpnUpdateFlags) lists() []bgpvpnListAttr {
	return []bgpvpnListAttr{
		{"route_targets", f.routeTargets, f.purgeRouteTargets, func(b *bgpvpns.BGPVPN) []string { return b.RouteTargets }},
		{"import_targets", f.importTargets, f.purgeImportTargets, func(b *bgpvpns.BGPVPN) []string { return b.ImportTargets }},
		{"export_targets", f.exportTargets, f.purgeExportTargets, func(b *bgpvpns.BGPVPN) []string { return b.ExportTargets }},
		{"route_distinguishers", f.routeDistinguishers, f.purgeRDs,
			func(b *bgpvpns.BGPVPN) []string { return b.RouteDistinguishers }},
	}
}

func newBGPVPNSetCommand(a *auth.Options, o *output.Options, unset bool) *cobra.Command {
	f := &bgpvpnUpdateFlags{unset: unset}
	verb, short, purge := "set", "Set BGP VPN properties", "no-"
	addHelp := "add a route target to the import/export list (repeatable)"
	if unset {
		verb, short, purge = "unset", "Unset BGP VPN properties", "all-"
		addHelp = "remove a route target from the import/export list (repeatable)"
	}
	cmd := &cobra.Command{
		Use:   verb + " " + bgpvpnRefArg,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPVPNUpdate(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	if !unset {
		fl.StringVar(&f.name, "name", "", "new name of the BGP VPN")
	}
	which := map[bool]string{false: "add", true: "remove"}[unset]
	fl.StringArrayVar(&f.routeTargets, flagBGPVPNRouteTarget, nil, addHelp)
	fl.BoolVar(&f.purgeRouteTargets, purge+flagBGPVPNRouteTarget, false, "empty the route target list")
	fl.StringArrayVar(&f.importTargets, flagBGPVPNImportTarget, nil, which+" an import route target (repeatable)")
	fl.BoolVar(&f.purgeImportTargets, purge+flagBGPVPNImportTarget, false, "empty the import route target list")
	fl.StringArrayVar(&f.exportTargets, flagBGPVPNExportTarget, nil, which+" an export route target (repeatable)")
	fl.BoolVar(&f.purgeExportTargets, purge+flagBGPVPNExportTarget, false, "empty the export route target list")
	fl.StringArrayVar(&f.routeDistinguishers, flagBGPVPNRouteDistg, nil, which+" a route distinguisher (repeatable)")
	fl.BoolVar(&f.purgeRDs, purge+flagBGPVPNRouteDistg, false, "empty the route distinguisher list")
	fl.IntVar(&f.vni, flagBGPVPNVNI, 0, bgpvpnVNIHelp)
	fl.IntVar(&f.localPref, flagBGPVPNLocalPref, 0, bgpvpnLocalPrefHelp)
	return cmd
}

// runBGPVPNUpdate is upstream's _args2body + update_bgpvpn. neutron replaces a
// list attribute whole, so an add or a removal is computed against the VPN as
// it is now — read only when a list flag is given and not every list is being
// purged, exactly as upstream decides it.
func runBGPVPNUpdate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *bgpvpnUpdateFlags, flags flagSet, w io.Writer,
) error {
	id, err := resolveBGPVPNID(ctx, client, ref)
	if err != nil {
		return err
	}
	body, err := buildBGPVPNUpdateBody(ctx, client, id, f, flags)
	if err != nil {
		return err
	}
	verb := map[bool]string{false: "set", true: "unset"}[f.unset]
	if len(body) == 0 {
		return fmt.Errorf("bgpvpn %s requires at least one attribute flag", verb)
	}
	b, err := bgpvpns.Update(ctx, client, id, body).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("updating BGP VPN %s: %w", ref, err), extBGPVPN)
	}
	fields, values := bgpvpnShowFields(b)
	return o.WriteSingle(w, fields, values)
}

func buildBGPVPNUpdateBody(ctx context.Context, client *gophercloud.ServiceClient, id string,
	f *bgpvpnUpdateFlags, flags flagSet,
) (bgpvpnBody, error) {
	unset := f.unset
	lists := f.lists()
	allPurged, anyGiven := true, false
	for _, l := range lists {
		allPurged = allPurged && l.purge
		anyGiven = anyGiven || len(l.given) > 0
	}
	current := &bgpvpns.BGPVPN{}
	if anyGiven && !allPurged {
		var err error
		if current, err = bgpvpns.Get(ctx, client, id).Extract(); err != nil {
			return nil, explainMissingService(ctx, client, fmt.Errorf("getting BGP VPN %s: %w", id, err), extBGPVPN)
		}
	}
	body := bgpvpnBody{}
	if !unset && flags.Changed("name") {
		body["name"] = f.name
	}
	if flags.Changed(flagBGPVPNVNI) {
		body["vni"] = f.vni
	}
	if flags.Changed(flagBGPVPNLocalPref) {
		body["local_pref"] = f.localPref
	}
	for _, l := range lists {
		switch {
		case l.purge:
			body[l.key] = []string{}
		case len(l.given) > 0 && unset:
			body[l.key] = keepUnmatched(l.current(current), func(v string) bool { return slices.Contains(l.given, v) })
		case len(l.given) > 0:
			body[l.key] = unionStrings(l.current(current), l.given)
		}
	}
	return body, nil
}

// unionStrings returns have plus every element of add not already in it, in
// order and without duplicates (upstream takes a set union).
func unionStrings(have, add []string) []string {
	out := make([]string, 0, len(have)+len(add))
	for _, v := range slices.Concat(have, add) {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// --- delete ------------------------------------------------------------------

func newBGPVPNDeleteCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete " + bgpvpnRefArg + " [" + bgpvpnRefArg + " ...]",
		Short: "Delete BGP VPN(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPVPNDelete(ctx, client, args)
		},
	}
}

func runBGPVPNDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveBGPVPNID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := bgpvpns.Delete(ctx, client, id).ExtractErr(); err != nil {
			return explainMissingService(ctx, client, fmt.Errorf("deleting BGP VPN %s: %w", ref, err), extBGPVPN)
		}
		return nil
	})
}
