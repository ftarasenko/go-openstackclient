package network

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "bgpvpn port association" — a port bound to a BGP VPN, optionally with
// prefix routes and BGP VPN (route-leaking) routes. The resource belongs to
// the bgpvpn-routes-control extension, so its errors name that one when the
// cloud lacks it (explainBGPVPNRoutesCtl).

const (
	flagAdvertiseFixedIPs   = "advertise-fixed-ips"
	flagNoAdvertiseFixedIPs = "no-advertise-fixed-ips"
	flagPrefixRoute         = "prefix-route"
	flagBGPVPNRoute         = "bgpvpn-route"
	portRouteTypePrefix     = "prefix"
	portRouteTypeBGPVPN     = "bgpvpn"
	portRouteKeyLocalPref   = "local_pref"
)

// bgpvpnPortAssocBody is a raw port_association body. The typed create opts
// drop an empty routes list, which upstream always sends.
type bgpvpnPortAssocBody map[string]any

func (b bgpvpnPortAssocBody) ToPortAssociationCreateMap() (map[string]any, error) {
	return map[string]any{"port_association": map[string]any(b)}, nil
}

func (b bgpvpnPortAssocBody) ToPortAssociationUpdateMap() (map[string]any, error) {
	return map[string]any{"port_association": map[string]any(b)}, nil
}

// bgpvpnPortAssocFlags backs create, set and unset. On create/set a route is
// "prefix=<cidr>[,local_pref=<n>]" / "bgpvpn=<bgpvpn>[,local_pref=<n>]"; on
// unset it is the bare CIDR / BGP VPN to remove.
type bgpvpnPortAssocFlags struct {
	adv          bgpvpnAdvertiseFlags
	prefixRoutes []string
	bgpvpnRoutes []string
	purgePrefix  bool
	purgeBGPVPN  bool
}

func bindBGPVPNPortAssocFlags(cmd *cobra.Command, f *bgpvpnPortAssocFlags, verb string) {
	fl := cmd.Flags()
	on, off := "advertise the port's fixed IPs to the BGP VPN", "do not advertise the port's fixed IPs to the BGP VPN"
	switch verb {
	case "create":
		on += " (default)"
	case "unset":
		on, off = off, on
	}
	fl.BoolVar(&f.adv.advertise, flagAdvertiseFixedIPs, false, on)
	fl.BoolVar(&f.adv.noAdvertise, flagNoAdvertiseFixedIPs, false, off)
	cmd.MarkFlagsMutuallyExclusive(flagAdvertiseFixedIPs, flagNoAdvertiseFixedIPs)
	if verb == "unset" {
		fl.StringArrayVar(&f.prefixRoutes, flagPrefixRoute, nil, "prefix route to remove, as a CIDR (repeatable)")
		fl.StringArrayVar(&f.bgpvpnRoutes, flagBGPVPNRoute, nil, "BGP VPN route to remove (name or ID; repeatable)")
		fl.BoolVar(&f.purgePrefix, "all-prefix-routes", false, "empty the prefix route list")
		fl.BoolVar(&f.purgeBGPVPN, "all-bgpvpn-routes", false, "empty the BGP VPN route list")
		return
	}
	fl.StringArrayVar(&f.prefixRoutes, flagPrefixRoute, nil,
		"prefix route to add, as prefix=<cidr>[,local_pref=<integer>] (repeatable)")
	fl.StringArrayVar(&f.bgpvpnRoutes, flagBGPVPNRoute, nil,
		"BGP VPN route to add for route leaking, as bgpvpn=<name or ID>[,local_pref=<integer>] (repeatable)")
	if verb == "set" {
		fl.BoolVar(&f.purgePrefix, "no-prefix-route", false, "empty the prefix route list")
		fl.BoolVar(&f.purgeBGPVPN, "no-bgpvpn-route", false, "empty the BGP VPN route list")
	}
}

// portRoute is one entry of a route list keyed by its prefix or BGP VPN ID.
type portRoute struct {
	key       string
	localPref *int
}

// portRouteList is an insertion-ordered route map: upstream keeps the routes
// in a dict, so an existing route keeps its place and an update replaces its
// local_pref.
type portRouteList []portRoute

func (l portRouteList) upsert(r portRoute) portRouteList {
	for i := range l {
		if l[i].key == r.key {
			l[i] = r
			return l
		}
	}
	return append(l, r)
}

func (l portRouteList) remove(key string) portRouteList {
	return keepUnmatched(l, func(r portRoute) bool { return r.key == key })
}

// parsePortRouteSpec parses "<key>=<value>[,local_pref=<integer>]" — osc-lib's
// MultiKeyValueAction with one required key and local_pref optional.
func parsePortRouteSpec(flag, required, spec string) (portRoute, error) {
	var r portRoute
	seen := false
	for _, part := range strings.Split(spec, ",") {
		k, v, err := splitKV(part)
		if err != nil {
			return r, fmt.Errorf("--%s %q: %w", flag, spec, err)
		}
		switch k {
		case required:
			r.key, seen = v, true
		case portRouteKeyLocalPref:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return r, fmt.Errorf("--%s %q: local_pref must be an integer", flag, spec)
			}
			r.localPref = &n
		default:
			return r, fmt.Errorf("--%s %q: unknown key %q (want %s, %s)", flag, spec, k, required, portRouteKeyLocalPref)
		}
	}
	if !seen || r.key == "" {
		return r, fmt.Errorf("--%s %q: %s= is required", flag, spec, required)
	}
	return r, nil
}

// currentPortRoutes splits an association's routes by type.
func currentPortRoutes(routes []bgpvpns.PortRoutes) (prefixes, vpns portRouteList) {
	for _, r := range routes {
		switch r.Type {
		case portRouteTypePrefix:
			prefixes = append(prefixes, portRoute{key: r.Prefix, localPref: r.LocalPref})
		case portRouteTypeBGPVPN:
			vpns = append(vpns, portRoute{key: r.BGPVPNID, localPref: r.LocalPref})
		}
	}
	return prefixes, vpns
}

// editPrefixRoutes applies --prefix-route to the current prefix routes.
func editPrefixRoutes(have portRouteList, specs []string, unset bool) (portRouteList, error) {
	for _, spec := range specs {
		if unset {
			have = have.remove(spec)
			continue
		}
		r, err := parsePortRouteSpec(flagPrefixRoute, portRouteTypePrefix, spec)
		if err != nil {
			return nil, err
		}
		have = have.upsert(r)
	}
	return have, nil
}

// editBGPVPNRoutes applies --bgpvpn-route to the current BGP VPN routes,
// resolving every named BGP VPN to its ID.
func editBGPVPNRoutes(ctx context.Context, client *gophercloud.ServiceClient, have portRouteList,
	specs []string, unset bool,
) (portRouteList, error) {
	for _, spec := range specs {
		r := portRoute{key: spec}
		if !unset {
			var err error
			if r, err = parsePortRouteSpec(flagBGPVPNRoute, portRouteTypeBGPVPN, spec); err != nil {
				return nil, err
			}
		}
		id, err := resolveBGPVPNID(ctx, client, r.key)
		if err != nil {
			return nil, err
		}
		r.key = id
		if unset {
			have = have.remove(id)
		} else {
			have = have.upsert(r)
		}
	}
	return have, nil
}

// buildPortAssocBody is upstream's _args2body: advertise_fixed_ips only when
// a flag says so, and routes always — the current routes (none on create)
// edited by the route flags, prefix routes first, [] when nothing is left.
func buildPortAssocBody(ctx context.Context, client *gophercloud.ServiceClient, f *bgpvpnPortAssocFlags,
	current []bgpvpns.PortRoutes, unset bool,
) (bgpvpnPortAssocBody, error) {
	body := bgpvpnPortAssocBody{}
	if v := f.adv.value(unset); v != nil {
		body["advertise_fixed_ips"] = *v
	}
	prefixes, vpns := currentPortRoutes(current)
	if f.purgePrefix {
		prefixes = nil
	}
	if f.purgeBGPVPN {
		vpns = nil
	}
	var err error
	if !f.purgePrefix {
		if prefixes, err = editPrefixRoutes(prefixes, f.prefixRoutes, unset); err != nil {
			return nil, err
		}
	}
	if !f.purgeBGPVPN {
		if vpns, err = editBGPVPNRoutes(ctx, client, vpns, f.bgpvpnRoutes, unset); err != nil {
			return nil, err
		}
	}
	routes := make([]bgpvpns.PortRoutes, 0, len(prefixes)+len(vpns))
	for _, r := range prefixes {
		routes = append(routes, bgpvpns.PortRoutes{Type: portRouteTypePrefix, Prefix: r.key, LocalPref: r.localPref})
	}
	for _, r := range vpns {
		routes = append(routes, bgpvpns.PortRoutes{Type: portRouteTypeBGPVPN, BGPVPNID: r.key, LocalPref: r.localPref})
	}
	body["routes"] = routes
	return body, nil
}

// portAssocRouteColumns is upstream's _transform_resource: the routes split
// into "<prefix> (<local_pref>)" and "<bgpvpn id> (<local_pref>)" lists, the
// local_pref left out when unset or zero. (Upstream's format string never
// interpolates it, so its output reads a literal "({local_pref})"; koc prints
// the value.)
func portAssocRouteColumns(routes []bgpvpns.PortRoutes) (prefixes, vpns []string) {
	label := func(key string, lp *int) string {
		if lp != nil && *lp != 0 {
			return fmt.Sprintf("%s (%d)", key, *lp)
		}
		return key
	}
	for _, r := range routes {
		switch r.Type {
		case portRouteTypePrefix:
			prefixes = append(prefixes, label(r.Prefix, r.LocalPref))
		case portRouteTypeBGPVPN:
			vpns = append(vpns, label(r.BGPVPNID, r.LocalPref))
		}
	}
	return prefixes, vpns
}

func writeBGPVPNPortAssoc(o *output.Options, w io.Writer, p *bgpvpns.PortAssociation) error {
	prefixes, vpns := portAssocRouteColumns(p.Routes)
	return o.WriteSingle(w,
		[]string{"advertise_fixed_ips", "bgpvpn_routes", "id", "port_id", "prefix_routes", "project_id"},
		[]any{p.AdvertiseFixedIPs, vpns, p.ID, p.PortID, prefixes, p.ProjectID})
}

func newBGPVPNPortAssocCommand(a *auth.Options, o *output.Options) *cobra.Command {
	kind := bgpvpnPortAssoc
	cf := &bgpvpnAssocCreateFlags{}
	pf := &bgpvpnPortAssocFlags{}
	create := newBGPVPNAssocCreateCommand(a, o, kind, cf,
		func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnRef, portRef string, _ flagSet, w io.Writer) error {
			return runBGPVPNPortAssocCreate(ctx, client, o, bgpvpnAssocRef{bgpvpnRef, portRef}, cf.projectID, pf, w)
		})
	bindBGPVPNPortAssocFlags(create, pf, "create")
	return newBGPVPNAssocParent(kind,
		create,
		newBGPVPNAssocDeleteCommand(a, kind, func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnID, id string) error {
			return bgpvpns.DeletePortAssociation(ctx, client, bgpvpnID, id).ExtractErr()
		}),
		newBGPVPNAssocListCommand(a, o, kind, runBGPVPNPortAssocList),
		newBGPVPNPortAssocSetCommand(a, o, false),
		newBGPVPNAssocShowCommand(a, o, kind, runBGPVPNPortAssocShow),
		newBGPVPNPortAssocSetCommand(a, o, true),
	)
}

func runBGPVPNPortAssocCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref bgpvpnAssocRef, projectID string, f *bgpvpnPortAssocFlags, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, ref.bgpvpn)
	if err != nil {
		return err
	}
	portID, err := resolvePortID(ctx, client, ref.target)
	if err != nil {
		return err
	}
	body, err := buildPortAssocBody(ctx, client, f, nil, false)
	if err != nil {
		return err
	}
	body["port_id"] = portID
	if projectID != "" {
		body["project_id"] = projectID
	}
	p, err := bgpvpns.CreatePortAssociation(ctx, client, bgpvpnID, body).Extract()
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("creating port association: %w", err))
	}
	return writeBGPVPNPortAssoc(o, w, p)
}

func newBGPVPNPortAssocSetCommand(a *auth.Options, o *output.Options, unset bool) *cobra.Command {
	f := &bgpvpnPortAssocFlags{}
	verb, short := "set", "Set BGP VPN port association properties"
	if unset {
		verb, short = "unset", "Unset BGP VPN port association properties"
	}
	cmd := &cobra.Command{
		Use:   verb + " " + bgpvpnPortAssoc.idArg() + " " + bgpvpnRefArg,
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPVPNPortAssocUpdate(ctx, client, o, bgpvpnAssocRef{args[1], args[0]}, f, unset, cmd.OutOrStdout())
		},
	}
	bindBGPVPNPortAssocFlags(cmd, f, verb)
	return cmd
}

// runBGPVPNPortAssocUpdate reads the association first, as upstream does:
// neutron replaces the routes list whole, so the edit is computed client-side.
func runBGPVPNPortAssocUpdate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref bgpvpnAssocRef, f *bgpvpnPortAssocFlags, unset bool, w io.Writer,
) error {
	id := ref.target
	bgpvpnID, err := resolveBGPVPNID(ctx, client, ref.bgpvpn)
	if err != nil {
		return err
	}
	current, err := bgpvpns.GetPortAssociation(ctx, client, bgpvpnID, id).Extract()
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("getting port association %s: %w", id, err))
	}
	body, err := buildPortAssocBody(ctx, client, f, current.Routes, unset)
	if err != nil {
		return err
	}
	p, err := bgpvpns.UpdatePortAssociation(ctx, client, bgpvpnID, id, body).Extract()
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("updating port association %s: %w", id, err))
	}
	return writeBGPVPNPortAssoc(o, w, p)
}

func runBGPVPNPortAssocShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	bgpvpnRef, id string, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, bgpvpnRef)
	if err != nil {
		return err
	}
	p, err := bgpvpns.GetPortAssociation(ctx, client, bgpvpnID, id).Extract()
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("showing port association %s: %w", id, err))
	}
	return writeBGPVPNPortAssoc(o, w, p)
}

func runBGPVPNPortAssocList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	bgpvpnRef string, f *bgpvpnAssocListFlags, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, bgpvpnRef)
	if err != nil {
		return err
	}
	q, err := assocListQuery(f)
	if err != nil {
		return err
	}
	pages, err := bgpvpns.ListPortAssociations(client, bgpvpnID, q).AllPages(ctx)
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("listing port associations: %w", err))
	}
	all, err := bgpvpns.ExtractPortAssociations(pages)
	if err != nil {
		return fmt.Errorf("parsing the port association list: %w", err)
	}
	cols := []string{"ID", "Port ID"}
	if f.long {
		cols = []string{
			"ID", "Project", "Port ID", "Prefix Routes (BGP LOCAL_PREF)",
			"BGP VPN Routes (BGP LOCAL_PREF)", "Advertise Port's Fixed IPs",
		}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		if !f.long {
			t.Rows = append(t.Rows, []any{p.ID, p.PortID})
			continue
		}
		prefixes, vpns := portAssocRouteColumns(p.Routes)
		t.Rows = append(t.Rows, []any{p.ID, p.ProjectID, p.PortID, prefixes, vpns, p.AdvertiseFixedIPs})
	}
	return o.WriteList(w, t)
}
