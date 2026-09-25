package network

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/allprojects"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newRouterCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "router",
		Short: "Manage routers",
	}
	cmd.AddCommand(newRouterListCommand(a, o))
	cmd.AddCommand(newRouterShowCommand(a, o))
	cmd.AddCommand(newRouterCreateCommand(a, o))
	cmd.AddCommand(newRouterDeleteCommand(a, o))
	cmd.AddCommand(newRouterSetCommand(a, o))
	cmd.AddCommand(newRouterUnsetCommand(a, o))
	cmd.AddCommand(newRouterAddCommand(a, o))
	cmd.AddCommand(newRouterRemoveCommand(a, o))
	return cmd
}

func routerShowFields(r *routers.Router) ([]string, []any) {
	fields := []string{
		"id", "name", "status", "admin_state_up", "distributed",
		"external_gateway_info", "routes", "description", "project_id",
		"tags", "created_at", "updated_at",
	}
	values := []any{
		r.ID, r.Name, r.Status, r.AdminStateUp, r.Distributed,
		r.GatewayInfo, r.Routes, r.Description, r.ProjectID,
		r.Tags, r.CreatedAt, r.UpdatedAt,
	}
	return fields, values
}

// routerDetailFields is routerShowFields plus the attributes upstream's
// ShowRouter also prints that gophercloud's Router does not model: ha (shown
// only when neutron sent it, as upstream hides it when None — it is
// admin-only by policy), availability_zone_hints, availability_zones,
// flavor_id and enable_ndp_proxy (always, empty on a cloud without
// l3-ext-ndp-proxy, as upstream), then evpn_vni only when set (upstream hides
// it when None) and the post-Zed default-route BFD/ECMP switches only when
// neutron sent them — upstream's SDK does not model those two, so on a cloud
// without the extensions nothing changes. The router verbs this file owns
// render through it; the extraroute verbs in extensions.go keep the plain
// field set.
func routerDetailFields(r *routers.Router, ext routerExtAttrs) ([]string, []any) {
	fields, values := routerShowFields(r)
	if ext.HA != nil {
		fields = append(fields, "ha")
		values = append(values, *ext.HA)
	}
	fields = append(fields, "availability_zone_hints", "availability_zones", "flavor_id", "enable_ndp_proxy")
	values = append(values, r.AvailabilityZoneHints, derefOrNil(ext.AvailabilityZones), derefOrNil(ext.FlavorID),
		derefOrNil(ext.EnableNDPProxy))
	for _, opt := range []struct {
		name  string
		value any
		set   bool
	}{
		{"enable_default_route_bfd", derefOrNil(ext.EnableDefaultRouteBFD), ext.EnableDefaultRouteBFD != nil},
		{"enable_default_route_ecmp", derefOrNil(ext.EnableDefaultRouteECMP), ext.EnableDefaultRouteECMP != nil},
		{"evpn_vni", derefOrNil(ext.EVPNVNI), ext.EVPNVNI != nil},
	} {
		if opt.set {
			fields = append(fields, opt.name)
			values = append(values, opt.value)
		}
	}
	return fields, values
}

// extractRouterDetail decodes a single-router response (get, create or
// update) into the typed Router and its routerExtAttrs. As with the list, the
// body is decoded twice because Router's UnmarshalJSON would swallow the rest.
func extractRouterDetail(res gophercloud.Result) (*routers.Router, routerExtAttrs, error) {
	var (
		r   routers.Router
		ext routerExtAttrs
	)
	if err := res.ExtractIntoStructPtr(&r, "router"); err != nil {
		return nil, ext, err
	}
	if err := res.ExtractIntoStructPtr(&ext, "router"); err != nil {
		return nil, ext, err
	}
	return &r, ext, nil
}

// writeRouterDetail renders one router through routerDetailFields.
func writeRouterDetail(o *output.Options, w io.Writer, r *routers.Router, ext routerExtAttrs) error {
	fields, values := routerDetailFields(r, ext)
	return o.WriteSingle(w, fields, values)
}

// routerListFlags holds the filters accepted by "router list". Upstream OSC's
// parser (network/v2/router.py ListRouter) is the reference; --all-projects is
// the one koc-native addition — see allProjectsNetworkList.
type routerListFlags struct {
	name          string
	project       string
	projectDomain string
	enable        bool
	disable       bool
	agent         string
	long          bool
	allProjects   bool
	tagFilterFlags

	adminStateUp *bool
}

func newRouterListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &routerListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List routers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, "enable", "disable"); err != nil {
				return err
			}
			f.adminStateUp = enableDisable(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runRouterList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "list only routers with this name")
	fl.StringVar(&f.project, "project", "", "list only routers owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only enabled routers (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "list only disabled routers (admin state down)")
	bindTagFilterFlags(fl, &f.tagFilterFlags, "routers")
	fl.StringVar(&f.agent, "agent", "", "list only routers hosted by this L3 agent (ID only)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	allprojects.Bind(cmd, &f.allProjects, allProjectsNetworkList)
	cmd.MarkFlagsMutuallyExclusive("project", "all-projects")
	return cmd
}

// allProjectsNetworkList is the --all-projects help text for the neutron list
// verbs whose table already carries the Project column. As with port list
// (allProjectsPortList), neutron has no cross-project switch — an admin token
// already lists every project's resources — so the flag changes nothing on the
// wire. It is accepted so a script that reaches for it is not rejected with a
// usage error, which is exactly how a missing filter used to go unnoticed.
const allProjectsNetworkList = "list across all projects (admin); an admin token already sees them all, " +
	"so this is accepted for compatibility and changes nothing"

// routerExtAttrs carries the router attributes whose *presence* decides a list
// column. gophercloud's Router models distributed as a plain bool and omits ha
// and availability_zones, but neutron sends distributed/ha only to an admin
// (policy) and availability_zones only with the router_availability_zone
// extension — upstream adds the Distributed/HA/Availability zones columns only
// when the attribute came back, so absence has to stay distinguishable.
//
// The post-Zed attributes ride along for router show: each is a pointer so a
// cloud without its extension renders the field empty (or not at all) rather
// than as a zero value.
type routerExtAttrs struct {
	Distributed       *bool     `json:"distributed"`
	HA                *bool     `json:"ha"`
	AvailabilityZones *[]string `json:"availability_zones"`
	FlavorID          *string   `json:"flavor_id"`

	EnableNDPProxy         *bool `json:"enable_ndp_proxy"`
	EnableDefaultRouteBFD  *bool `json:"enable_default_route_bfd"`
	EnableDefaultRouteECMP *bool `json:"enable_default_route_ecmp"`
	EVPNVNI                *int  `json:"evpn_vni"`
}

// routerListRow pairs a router with its routerExtAttrs. The page is decoded
// twice rather than into one struct embedding both, because the two would
// share the `distributed` key and Router's own UnmarshalJSON would swallow the
// rest.
type routerListRow struct {
	routers.Router
	ext routerExtAttrs
}

// extractRouterRows decodes one routers body into rows. extract is the
// page/result's slice extractor, called once per target.
func extractRouterRows(extract func(v any) error) ([]routerListRow, error) {
	var base []routers.Router
	var ext []routerExtAttrs
	if err := extract(&base); err != nil {
		return nil, err
	}
	if err := extract(&ext); err != nil {
		return nil, err
	}
	rows := make([]routerListRow, len(base))
	for i := range base {
		rows[i].Router = base[i]
		if i < len(ext) {
			rows[i].ext = ext[i]
		}
	}
	return rows, nil
}

func runRouterList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *routerListFlags, projectID string, w io.Writer,
) error {
	opts := routers.ListOpts{
		Name:         f.name,
		ProjectID:    projectID,
		AdminStateUp: f.adminStateUp,
	}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
	if f.agent != "" {
		// The agent's l3-routers subresource takes no query filters (upstream
		// notes the same), so every filter is re-applied client-side.
		res := agents.ListL3Routers(ctx, client, f.agent)
		hosted, err := extractRouterRows(func(v any) error { return res.ExtractIntoSlicePtr(v, "routers") })
		if err != nil {
			return fmt.Errorf("listing routers hosted by agent %s: %w", f.agent, err)
		}
		all := make([]routerListRow, 0, len(hosted))
		for _, r := range hosted {
			if routerMatches(r.Router, opts) {
				all = append(all, r)
			}
		}
		return o.WriteList(w, routerListTable(all, f.long))
	}
	pages, err := routers.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing routers: %w", err)
	}
	all, err := extractRouterRows(func(v any) error { return routers.ExtractRoutersInto(pages, v) })
	if err != nil {
		return fmt.Errorf("parsing router list: %w", err)
	}
	return o.WriteList(w, routerListTable(all, f.long))
}

// routerListTable lays out upstream's columns: the five fixed ones, then
// Distributed and HA when any router carried them, then --long's Routes,
// External gateway info, Availability zones (when the extension reported them)
// and Tags.
func routerListTable(all []routerListRow, long bool) output.Table {
	var distributed, ha, azs bool
	for _, r := range all {
		distributed = distributed || r.ext.Distributed != nil
		ha = ha || r.ext.HA != nil
		azs = azs || r.ext.AvailabilityZones != nil
	}
	cols := []string{"ID", "Name", "Status", "State", "Project"}
	if distributed {
		cols = append(cols, "Distributed")
	}
	if ha {
		cols = append(cols, "HA")
	}
	if long {
		cols = append(cols, "Routes", "External gateway info")
		if azs {
			cols = append(cols, "Availability zones")
		}
		cols = append(cols, "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		row := []any{r.ID, r.Name, r.Status, adminState(r.AdminStateUp), r.ProjectID}
		if distributed {
			row = append(row, derefOrNil(r.ext.Distributed))
		}
		if ha {
			row = append(row, derefOrNil(r.ext.HA))
		}
		if long {
			row = append(row, r.Routes, r.GatewayInfo)
			if azs {
				row = append(row, derefOrNil(r.ext.AvailabilityZones))
			}
			row = append(row, r.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// derefOrNil renders an absent optional attribute as an empty cell rather than
// as the type's zero value, which would read as a real false/empty list.
func derefOrNil[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// routerMatches applies the router list filters to one router, for the
// --agent path where neutron cannot. Tag semantics follow neutron's: tags =
// all of, tags-any = any of, not-tags = not all of, not-tags-any = none of.
func routerMatches(r routers.Router, opts routers.ListOpts) bool {
	switch {
	case opts.Name != "" && r.Name != opts.Name,
		opts.ProjectID != "" && r.ProjectID != opts.ProjectID,
		opts.AdminStateUp != nil && r.AdminStateUp != *opts.AdminStateUp:
		return false
	}
	hasAll := func(csv string) bool {
		for _, t := range strings.Split(csv, ",") {
			if !slices.Contains(r.Tags, t) {
				return false
			}
		}
		return true
	}
	hasAny := func(csv string) bool {
		return slices.ContainsFunc(strings.Split(csv, ","), func(t string) bool { return slices.Contains(r.Tags, t) })
	}
	switch {
	case opts.Tags != "" && !hasAll(opts.Tags),
		opts.TagsAny != "" && !hasAny(opts.TagsAny),
		opts.NotTags != "" && hasAll(opts.NotTags),
		opts.NotTagsAny != "" && hasAny(opts.NotTagsAny):
		return false
	}
	return true
}

func newRouterShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <router>",
		Short: "Show details of a router",
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
			return runRouterShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runRouterShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveRouterID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	r, ext, err := extractRouterDetail(routers.Get(ctx, client, id).Result)
	if err != nil {
		return fmt.Errorf("getting router %s: %w", nameOrID, err)
	}
	interfaces, err := routerInterfaces(ctx, client, r.ID)
	if err != nil {
		return err
	}
	fields, values := routerDetailFields(r, ext)
	fields = append(fields, "interfaces_info")
	values = append(values, interfaces)
	return o.WriteSingle(w, fields, values)
}

// routerInterface is one entry of router show's interfaces_info: a single fixed
// IP of one of the router's internal ports. The field order matches upstream's
// dict, so the rendered JSON is byte-identical to `openstack router show`.
type routerInterface struct {
	PortID    string `json:"port_id"`
	IPAddress string `json:"ip_address"`
	SubnetID  string `json:"subnet_id"`
}

// routerInterfaces derives interfaces_info the way upstream OSC's ShowRouter
// does: neutron's router body carries no interface list, so the router's ports
// are listed by device_id and every fixed IP of each non-gateway port becomes an
// entry. It is one extra GET, which is what `router show` costs upstream too.
func routerInterfaces(ctx context.Context, client *gophercloud.ServiceClient, routerID string) ([]routerInterface, error) {
	pages, err := ports.List(client, ports.ListOpts{DeviceID: routerID}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing ports of router %s: %w", routerID, err)
	}
	all, err := ports.ExtractPorts(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing ports of router %s: %w", routerID, err)
	}
	out := make([]routerInterface, 0, len(all))
	for _, p := range all {
		if p.DeviceOwner == "network:router_gateway" {
			continue
		}
		for _, ip := range p.FixedIPs {
			out = append(out, routerInterface{PortID: p.ID, IPAddress: ip.IPAddress, SubnetID: ip.SubnetID})
		}
	}
	return out, nil
}

// Router flag names. They live here rather than in flagnames.go because only
// the router verbs use them (see the note in flagnames.go on why each name is
// a constant: pflag reports a mistyped lookup as "not set").
const (
	flagRouterDistributed     = "distributed"
	flagRouterCentralized     = "centralized"
	flagRouterHA              = "ha"
	flagRouterNoHA            = "no-ha"
	flagRouterExternalGateway = "external-gateway"
	flagRouterFlavor          = "flavor"
	flagRouterFlavorID        = "flavor-id"
	flagRouterRoute           = "route"

	// post-Zed router flags (neutron 2023.1 onwards; see attrExtensions)
	flagRouterEnableNDPProxy  = "enable-ndp-proxy"
	flagRouterDisableNDPProxy = "disable-ndp-proxy"
	flagRouterEnableBFD       = "enable-default-route-bfd"
	flagRouterDisableBFD      = "disable-default-route-bfd"
	flagRouterEnableECMP      = "enable-default-route-ecmp"
	flagRouterDisableECMP     = "disable-default-route-ecmp"
	flagRouterEVPNVNI         = "evpn-vni"
	flagRouterAutoEVPNVNI     = "auto-evpn-vni"
	flagRouterAdvertiseHost   = "advertise-host"
)

// routerMultihomingAlias is the extension upstream's
// _command_check_bfd_ecmp_supported demands before sending either
// default-route switch; neutron-lib lists it as a required extension of both.
const routerMultihomingAlias = "external-gateway-multihoming"

// routerPostZedFlags are the post-Zed switches create and set share. Each pair
// lands in the body only when one half was given.
type routerPostZedFlags struct {
	enableNDPProxy  bool
	disableNDPProxy bool
	enableBFD       bool
	disableBFD      bool
	enableECMP      bool
	disableECMP     bool
}

// bindRouterPostZedFlags registers the NDP-proxy and default-route BFD/ECMP
// pairs. Upstream lets the later of --enable-/--disable-default-route-* win;
// koc refuses the pair together, as it does every other on/off pair.
func bindRouterPostZedFlags(cmd *cobra.Command, f *routerPostZedFlags, ndpNote string) {
	fl := cmd.Flags()
	fl.BoolVar(&f.enableNDPProxy, flagRouterEnableNDPProxy, false,
		"enable IPv6 NDP proxy on the external gateway"+ndpNote+" (requires the l3-ext-ndp-proxy extension)")
	fl.BoolVar(&f.disableNDPProxy, flagRouterDisableNDPProxy, false,
		"disable IPv6 NDP proxy on the external gateway (requires the l3-ext-ndp-proxy extension)")
	fl.BoolVar(&f.enableBFD, flagRouterEnableBFD, false,
		"enable BFD sessions for the default routes inferred from the gateway subnets "+
			"(requires the enable-default-route-bfd extension)")
	fl.BoolVar(&f.disableBFD, flagRouterDisableBFD, false,
		"disable BFD sessions for the gateway's default routes (requires the enable-default-route-bfd extension)")
	fl.BoolVar(&f.enableECMP, flagRouterEnableECMP, false,
		"add ECMP default routes when several gateway ports offer one (requires the enable-default-route-ecmp extension)")
	fl.BoolVar(&f.disableECMP, flagRouterDisableECMP, false,
		"add a default route for the first gateway port only (requires the enable-default-route-ecmp extension)")
	cmd.MarkFlagsMutuallyExclusive(flagRouterEnableNDPProxy, flagRouterDisableNDPProxy)
	cmd.MarkFlagsMutuallyExclusive(flagRouterEnableBFD, flagRouterDisableBFD)
	cmd.MarkFlagsMutuallyExclusive(flagRouterEnableECMP, flagRouterDisableECMP)
}

// bfdECMPAttrs returns the default-route BFD/ECMP attributes, which upstream
// collects in _get_attrs — before --extra-property, so that still wins.
func (f *routerPostZedFlags) bfdECMPAttrs() map[string]any {
	var attrs map[string]any
	if v := onOff(f.enableBFD, f.disableBFD); v != nil {
		attrs = mergeAttrs(attrs, map[string]any{"enable_default_route_bfd": *v})
	}
	if v := onOff(f.enableECMP, f.disableECMP); v != nil {
		attrs = mergeAttrs(attrs, map[string]any{"enable_default_route_ecmp": *v})
	}
	return attrs
}

// ndpProxyAttrs returns enable_ndp_proxy, which upstream sets after
// --extra-property, so it wins over an extra property of the same name.
func (f *routerPostZedFlags) ndpProxyAttrs() map[string]any {
	if v := onOff(f.enableNDPProxy, f.disableNDPProxy); v != nil {
		return map[string]any{"enable_ndp_proxy": *v}
	}
	return nil
}

// onOff resolves a pair cobra already keeps from being given together: true,
// false, or nil when neither was set.
func onOff(on, off bool) *bool {
	switch {
	case on:
		return boolPtr(true)
	case off:
		return boolPtr(false)
	}
	return nil
}

// checkBFDECMPSupported mirrors upstream's _command_check_bfd_ecmp_supported:
// a default-route BFD/ECMP attribute in the final body (from a flag or an
// --extra-property) is refused before anything is sent unless neutron has the
// external-gateway-multihoming extension.
func checkBFDECMPSupported(ctx context.Context, client *gophercloud.ServiceClient, attrs map[string]any) error {
	_, bfd := attrs["enable_default_route_bfd"]
	_, ecmp := attrs["enable_default_route_ecmp"]
	if !bfd && !ecmp {
		return nil
	}
	exts, err := listNetworkExtensions(ctx, client)
	if err != nil {
		return fmt.Errorf("checking for the %s extension: %w", routerMultihomingAlias, err)
	}
	if !slices.ContainsFunc(exts, func(e networkExtension) bool { return e.Alias == routerMultihomingAlias }) {
		return fmt.Errorf("the %s extension is not enabled in this cloud's neutron, so --%s and --%s cannot be used",
			routerMultihomingAlias, flagRouterEnableBFD, flagRouterEnableECMP)
	}
	return nil
}

// parseEVPNVNI mirrors upstream's _parse_evpn_vni: --evpn-vni takes a positive
// integer; 0 is what --auto-evpn-vni sends.
func parseEVPNVNI(value string) (int, error) {
	vni, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("--%s %q is not a valid VNI (use a positive integer)", flagRouterEVPNVNI, value)
	}
	if vni <= 0 {
		return 0, fmt.Errorf("--%s: VNI must be a positive integer", flagRouterEVPNVNI)
	}
	return vni, nil
}

type routerCreateFlags struct {
	enable          bool
	disable         bool
	description     string
	distributed     bool
	centralized     bool
	ha              bool
	noHA            bool
	azHints         []string
	externalGateway string
	fixedIPs        []string
	enableSNAT      bool
	disableSNAT     bool
	qosPolicy       string
	flavor          string
	flavorID        string
	project         string
	projectDomain   string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	extraProperty []string
	// evpnVNI is --evpn-vni as typed ("" when not given), parsed by the seam
	// so a non-integer gets upstream's message; autoEVPNVNI sends 0.
	evpnVNI     string
	autoEVPNVNI bool
	routerPostZedFlags
	tagWriteFlags
}

// newRouterCreateCommand builds "router create". Upstream's CreateRouter is
// the reference, including its post-Zed flags (--enable/--disable-ndp-proxy,
// the default-route BFD/ECMP pairs, --evpn-vni/--auto-evpn-vni), which a Zed
// cloud answers with a 400 that explainMissingExtension names.
// --external-gateway takes one network — several need the
// external-gateway-multihoming extension, which Zed does not have.
func newRouterCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &routerCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new router",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			return runRouterCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the router (admin state up, default)")
	fl.BoolVar(&f.disable, "disable", false, "disable the router (admin state down)")
	fl.StringVar(&f.description, flagDescription, "", "description for the router")
	fl.BoolVar(&f.distributed, flagRouterDistributed, false, "create a distributed (DVR) router")
	fl.BoolVar(&f.centralized, flagRouterCentralized, false, "create a centralized router")
	fl.BoolVar(&f.ha, flagRouterHA, false, "create a highly available router")
	fl.BoolVar(&f.noHA, flagRouterNoHA, false, "create a legacy (non-HA) router")
	fl.StringArrayVar(&f.azHints, flagAvailabilityZoneHint, nil,
		"availability zone to schedule the router in (repeatable; router_availability_zone extension)")
	fl.StringVar(&f.externalGateway, flagRouterExternalGateway, "", "external network for the router's gateway (name or ID)")
	fl.StringArrayVar(&f.fixedIPs, flagFixedIP, nil,
		"gateway address as subnet=<name|id>[,ip-address=<ip>] (repeatable; needs --external-gateway)")
	fl.BoolVar(&f.enableSNAT, flagEnableSNAT, false, "enable source NAT on the external gateway")
	fl.BoolVar(&f.disableSNAT, flagDisableSNAT, false, "disable source NAT on the external gateway")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy for the gateway IPs (name or ID; needs --external-gateway)")
	fl.StringVar(&f.flavor, flagRouterFlavor, "", "router flavor (ID; koc cannot look a flavor up by name yet)")
	fl.StringVar(&f.flavorID, flagRouterFlavorID, "", "router flavor ID (deprecated alias of --flavor)")
	_ = fl.MarkHidden(flagRouterFlavorID)
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindRouterPostZedFlags(cmd, &f.routerPostZedFlags, ", needs --external-gateway")
	fl.StringVar(&f.evpnVNI, flagRouterEVPNVNI, "",
		"associate the router with the EVPN of this VNI, a positive integer (requires the evpn extension)")
	fl.BoolVar(&f.autoEVPNVNI, flagRouterAutoEVPNVNI, false,
		"associate the router with an EVPN on an auto-assigned VNI (requires the evpn extension)")
	cmd.MarkFlagsMutuallyExclusive(flagRouterEVPNVNI, flagRouterAutoEVPNVNI)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagCreateFlags(cmd, &f.tagWriteFlags, "router")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive(flagRouterDistributed, flagRouterCentralized)
	cmd.MarkFlagsMutuallyExclusive(flagRouterHA, flagRouterNoHA)
	cmd.MarkFlagsMutuallyExclusive(flagEnableSNAT, flagDisableSNAT)
	return cmd
}

func runRouterCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *routerCreateFlags, w io.Writer) error {
	// Upstream checks this only after the router exists, leaving it behind;
	// checking first creates nothing on a usage error.
	if f.externalGateway == "" && (f.enableSNAT || f.disableSNAT || len(f.fixedIPs) > 0 || f.qosPolicy != "") {
		return fmt.Errorf("--%s, --%s, --%s and --%s need --%s",
			flagEnableSNAT, flagDisableSNAT, flagFixedIP, flagQoSPolicy, flagRouterExternalGateway)
	}
	if f.externalGateway == "" && f.enableNDPProxy {
		return fmt.Errorf("--%s needs --%s", flagRouterEnableNDPProxy, flagRouterExternalGateway)
	}
	opts := routers.CreateOpts{
		Name:                  name,
		Description:           f.description,
		AdminStateUp:          boolPtr(!f.disable),
		ProjectID:             f.projectID,
		AvailabilityZoneHints: f.azHints,
	}
	switch {
	case f.distributed:
		opts.Distributed = boolPtr(true)
	case f.centralized:
		opts.Distributed = boolPtr(false)
	}
	gateway, err := buildCreateGatewayInfo(ctx, client, f)
	if err != nil {
		return err
	}
	opts.GatewayInfo = gateway
	attrs, err := routerCreateAttrs(f)
	if err != nil {
		return err
	}
	if err := checkBFDECMPSupported(ctx, client, attrs); err != nil {
		return err
	}
	r, ext, err := extractRouterDetail(routers.Create(ctx, client, withRouterCreateAttrs(opts, attrs)).Result)
	if err != nil {
		return explainMissingExtension(ctx, client, fmt.Errorf("creating router: %w", err), attrs)
	}
	// Tags cannot ride on the create; upstream sets them afterwards too.
	if r.Tags, err = applyTagsForSet(ctx, client, tagResourceRouters, r.ID, r.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	return writeRouterDetail(o, w, r, ext)
}

// buildCreateGatewayInfo assembles the create's external_gateway_info from
// --external-gateway and the flags that only mean something with it.
func buildCreateGatewayInfo(ctx context.Context, client *gophercloud.ServiceClient, f *routerCreateFlags) (*routers.GatewayInfo, error) {
	if f.externalGateway == "" {
		return nil, nil
	}
	networkID, err := resolveNetworkID(ctx, client, f.externalGateway)
	if err != nil {
		return nil, err
	}
	info := &routers.GatewayInfo{NetworkID: networkID}
	switch {
	case f.enableSNAT:
		info.EnableSNAT = boolPtr(true)
	case f.disableSNAT:
		info.EnableSNAT = boolPtr(false)
	}
	if info.ExternalFixedIPs, err = parseExternalFixedIPs(ctx, client, f.fixedIPs); err != nil {
		return nil, err
	}
	if f.qosPolicy != "" {
		if info.QoSPolicyID, err = resolveQoSPolicyID(ctx, client, f.qosPolicy); err != nil {
			return nil, err
		}
	}
	return info, nil
}

// routerCreateAttrs collects the create attributes routers.CreateOpts lacks —
// ha, flavor_id, the default-route BFD/ECMP switches and evpn_vni — then
// --extra-property, then enable_ndp_proxy, in upstream's order: its
// take_action sets the NDP proxy after the extra properties, so that one flag
// wins over them.
func routerCreateAttrs(f *routerCreateFlags) (map[string]any, error) {
	var attrs map[string]any
	switch {
	case f.ha:
		attrs = mergeAttrs(attrs, map[string]any{"ha": true})
	case f.noHA:
		attrs = mergeAttrs(attrs, map[string]any{"ha": false})
	}
	flavorID, err := routerFlavorID(f.flavor, f.flavorID)
	if err != nil {
		return nil, err
	}
	if flavorID != "" {
		attrs = mergeAttrs(attrs, map[string]any{"flavor_id": flavorID})
	}
	attrs = mergeAttrs(attrs, f.bfdECMPAttrs())
	switch {
	case f.autoEVPNVNI:
		attrs = mergeAttrs(attrs, map[string]any{"evpn_vni": 0})
	case f.evpnVNI != "":
		vni, err := parseEVPNVNI(f.evpnVNI)
		if err != nil {
			return nil, err
		}
		attrs = mergeAttrs(attrs, map[string]any{"evpn_vni": vni})
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(mergeAttrs(attrs, extra), f.ndpProxyAttrs()), nil
}

// routerFlavorID picks the flavor to send. Upstream resolves both --flavor and
// its hidden alias --flavor-id by name or ID (find_flavor), the alias winning.
// koc has no "network flavor" support to look a name up with, so the alias is
// sent as given and --flavor must be a UUID — a name is refused with a clear
// error rather than being sent as an ID neutron would reject.
func routerFlavorID(flavor, flavorID string) (string, error) {
	if flavorID != "" {
		return flavorID, nil
	}
	if flavor == "" || resolve.IsUUID(flavor) {
		return flavor, nil
	}
	return "", fmt.Errorf("--%s %q: pass the flavor's ID; koc cannot look a network flavor up by name yet",
		flagRouterFlavor, flavor)
}

func newRouterDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <router> [<router> ...]",
		Short: "Delete router(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runRouterDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runRouterDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	return batchdelete.Each(names, func(nameOrID string) error {
		id, err := resolveRouterID(ctx, client, nameOrID)
		if err != nil {
			return err
		}
		if err := routers.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting router %s: %w", nameOrID, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted router %s\n", nameOrID); err != nil {
			return err
		}
		return nil
	})
}

type routerSetFlags struct {
	name            string
	description     string
	enable          bool
	disable         bool
	distributed     bool
	centralized     bool
	ha              bool
	noHA            bool
	externalGateway string
	fixedIPs        []string

	enableSNAT    bool
	disableSNAT   bool
	qosPolicy     string
	noQoSPolicy   bool
	route         []string
	noRoute       bool
	extraProperty []string
	routerPostZedFlags
	tagWriteFlags
}

// newRouterSetCommand builds "router set". As on create, --external-gateway
// takes one network; the post-Zed NDP-proxy and default-route BFD/ECMP pairs
// are upstream's (evpn_vni is create-only in neutron — allow_put is false —
// so set has no --evpn-vni).
func newRouterSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &routerSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <router>",
		Short: "Set router properties",
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
			return runRouterSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new router name")
	fl.StringVar(&f.description, flagDescription, "", "new description for the router")
	fl.BoolVar(&f.enable, "enable", false, "enable the router (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "disable the router (admin state down)")
	fl.BoolVar(&f.distributed, flagRouterDistributed, false, "make the router distributed (disabled router only)")
	fl.BoolVar(&f.centralized, flagRouterCentralized, false, "make the router centralized (disabled router only)")
	fl.BoolVar(&f.ha, flagRouterHA, false, "make the router highly available (disabled router only)")
	fl.BoolVar(&f.noHA, flagRouterNoHA, false, "make the router a legacy (non-HA) router (disabled router only)")
	fl.StringVar(&f.externalGateway, flagRouterExternalGateway, "", "set the external gateway network (name or ID)")
	fl.StringArrayVar(&f.fixedIPs, flagFixedIP, nil,
		"gateway address as subnet=<name|id>[,ip-address=<ip>] (repeatable; replaces the gateway's fixed IPs)")
	fl.BoolVar(&f.enableSNAT, flagEnableSNAT, false, "enable source NAT on the external gateway")
	fl.BoolVar(&f.disableSNAT, flagDisableSNAT, false, "disable source NAT on the external gateway")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy to attach to the gateway IPs (name or ID)")
	fl.BoolVar(&f.noQoSPolicy, flagNoQoSPolicy, false, "detach the gateway IPs' QoS policy")
	fl.StringArrayVar(&f.route, flagRouterRoute, nil,
		"static route as destination=<cidr>,gateway=<ip> (repeatable; appends unless --no-route is also given)")
	fl.BoolVar(&f.noRoute, "no-route", false, "clear the router's static routes (with --route, replaces them instead)")
	bindRouterPostZedFlags(cmd, &f.routerPostZedFlags, "")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagSetFlags(fl, &f.tagWriteFlags, "router")
	cmd.MarkFlagsMutuallyExclusive(flagEnableSNAT, flagDisableSNAT)
	cmd.MarkFlagsMutuallyExclusive(flagRouterDistributed, flagRouterCentralized)
	cmd.MarkFlagsMutuallyExclusive(flagRouterHA, flagRouterNoHA)
	cmd.MarkFlagsMutuallyExclusive(flagQoSPolicy, flagNoQoSPolicy)
	return cmd
}

func runRouterSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *routerSetFlags, flags flagSet, w io.Writer) error {
	if err := mutuallyExclusive(flags, "enable", "disable"); err != nil {
		return err
	}
	id, err := resolveRouterID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts, attrs, err := buildRouterUpdate(ctx, client, nameOrID, id, f, flags)
	if err != nil {
		return err
	}
	if err := checkBFDECMPSupported(ctx, client, attrs); err != nil {
		return err
	}
	changed := opts != (routers.UpdateOpts{}) || len(attrs) > 0
	if !changed && !f.given() {
		return fmt.Errorf("router set requires at least one attribute flag")
	}
	return updateRouter(ctx, client, o, nameOrID, id, routerUpdate{opts: opts, attrs: attrs, changed: changed, action: "updating"},
		applyTagsForSet, &f.tagWriteFlags, w)
}

// buildRouterUpdate turns the set flags into the typed update plus the
// attributes UpdateOpts lacks (ha, the default-route BFD/ECMP switches, and
// external_gateway_info as a map so --no-qos-policy can send null), then
// --extra-property, then enable_ndp_proxy — upstream's order, which lets the
// NDP-proxy flag win over an extra property.
func buildRouterUpdate(ctx context.Context, client *gophercloud.ServiceClient,
	nameOrID, id string, f *routerSetFlags, flags flagSet,
) (routers.UpdateOpts, map[string]any, error) {
	opts := routers.UpdateOpts{
		Name:         f.name,
		AdminStateUp: enableDisable(flags, f.enable, f.disable),
		Distributed:  enableDisable(flags, f.distributed, f.centralized, flagRouterDistributed, flagRouterCentralized),
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
	}
	var attrs map[string]any
	if ha := enableDisable(flags, f.ha, f.noHA, flagRouterHA, flagRouterNoHA); ha != nil {
		attrs = mergeAttrs(attrs, map[string]any{"ha": *ha})
	}
	gateway, err := buildGatewayInfo(ctx, client, nameOrID, id, f, flags)
	if err != nil {
		return opts, nil, err
	}
	if gateway != nil {
		attrs = mergeAttrs(attrs, map[string]any{"external_gateway_info": gateway})
	}
	if opts.Routes, err = buildRoutes(ctx, client, nameOrID, id, f); err != nil {
		return opts, nil, err
	}
	attrs = mergeAttrs(attrs, f.bfdECMPAttrs())
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return opts, nil, err
	}
	return opts, mergeAttrs(mergeAttrs(attrs, extra), f.ndpProxyAttrs()), nil
}

// routerUpdate is one prepared router PUT: the typed options, the extra
// attributes merged over them, whether anything is to be sent at all, and the
// verb phrase an error names ("updating router r1: …").
type routerUpdate struct {
	opts    routers.UpdateOpts
	attrs   map[string]any
	changed bool
	action  string
}

// updateRouter is the shared tail of set and unset: PUT the attributes when
// any were given (upstream skips the update when only tags change, reading
// the router instead), then apply the tag change, then render the router.
func updateRouter(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, id string,
	u routerUpdate,
	applyTags func(context.Context, *gophercloud.ServiceClient, string, string, []string, *tagWriteFlags) ([]string, error),
	tags *tagWriteFlags, w io.Writer,
) error {
	var res gophercloud.Result
	action := u.action
	if u.changed {
		res = routers.Update(ctx, client, id, withRouterUpdateAttrs(u.opts, u.attrs)).Result
	} else {
		res, action = routers.Get(ctx, client, id).Result, "getting"
	}
	r, ext, err := extractRouterDetail(res)
	if err != nil {
		err = fmt.Errorf("%s router %s: %w", action, ref, err)
		if u.changed {
			err = explainMissingExtension(ctx, client, err, u.attrs)
		}
		return err
	}
	if r.Tags, err = applyTags(ctx, client, tagResourceRouters, id, r.Tags, tags); err != nil {
		return err
	}
	return writeRouterDetail(o, w, r, ext)
}

// buildGatewayInfo assembles external_gateway_info from --external-gateway,
// --fixed-ip, --enable-snat/--disable-snat and --qos-policy/--no-qos-policy,
// returning nil when none were given. It is a map rather than a
// routers.GatewayInfo because --no-qos-policy has to send qos_policy_id: null,
// which the typed struct's omitempty string cannot.
//
// Neutron replaces external_gateway_info wholesale and rejects it without a
// network_id, so changing the gateway without --external-gateway means
// re-sending the router's current gateway network — which is read back here
// rather than requiring the operator to repeat --external-gateway (upstream
// demands it for SNAT and fixed IPs; koc does not).
func buildGatewayInfo(ctx context.Context, client *gophercloud.ServiceClient,
	nameOrID, id string, f *routerSetFlags, flags flagSet,
) (map[string]any, error) {
	snat := enableDisable(flags, f.enableSNAT, f.disableSNAT, flagEnableSNAT, flagDisableSNAT)
	if f.externalGateway == "" && snat == nil && len(f.fixedIPs) == 0 && f.qosPolicy == "" && !f.noQoSPolicy {
		return nil, nil
	}
	fixed, err := parseExternalFixedIPs(ctx, client, f.fixedIPs)
	if err != nil {
		return nil, err
	}
	var qos any // nil is --no-qos-policy's null
	if f.qosPolicy != "" {
		if qos, err = resolveQoSPolicyID(ctx, client, f.qosPolicy); err != nil {
			return nil, err
		}
	}
	qosGiven := f.noQoSPolicy || f.qosPolicy != ""

	if f.externalGateway != "" {
		gwID, err := resolveNetworkID(ctx, client, f.externalGateway)
		if err != nil {
			return nil, err
		}
		return gatewayInfoBody(gwID, snat, fixed, qos, qosGiven), nil
	}

	current, err := routers.Get(ctx, client, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("reading router %s to preserve its external gateway: %w", nameOrID, err)
	}
	cur := current.GatewayInfo
	if cur.NetworkID == "" {
		return nil, fmt.Errorf("router %s has no external gateway: pass --%s alongside --%s/--%s/--%s/--%s/--%s",
			nameOrID, flagRouterExternalGateway, flagEnableSNAT, flagDisableSNAT, flagFixedIP, flagQoSPolicy, flagNoQoSPolicy)
	}
	if snat == nil && fixed == nil {
		// A QoS-only change sends what upstream sends: the current network and
		// the policy. Re-sending the fixed IPs or SNAT as well would trip
		// neutron's admin-only policy on those sub-attributes for a project user.
		return gatewayInfoBody(cur.NetworkID, nil, nil, qos, true), nil
	}
	// Keep what neutron already has for anything not being changed; omitting
	// the fixed IPs would make it reallocate the router's gateway address.
	if snat == nil {
		snat = cur.EnableSNAT
	}
	if fixed == nil {
		fixed = cur.ExternalFixedIPs
	}
	if !qosGiven && cur.QoSPolicyID != "" {
		qos, qosGiven = cur.QoSPolicyID, true
	}
	return gatewayInfoBody(cur.NetworkID, snat, fixed, qos, qosGiven), nil
}

// gatewayInfoBody lays out one external_gateway_info object. qos is sent only
// when withQoS is set, and then as given — a nil qos is JSON null.
func gatewayInfoBody(networkID string, snat *bool, fixed []routers.ExternalFixedIP, qos any, withQoS bool) map[string]any {
	gw := map[string]any{"network_id": networkID}
	if snat != nil {
		gw["enable_snat"] = *snat
	}
	if len(fixed) > 0 {
		gw["external_fixed_ips"] = fixed
	}
	if withQoS {
		gw["qos_policy_id"] = qos
	}
	return gw
}

// buildRoutes resolves the --route/--no-route pair into the routes list to send,
// or nil when neither was given. Matching OSC: --no-route alone clears the list,
// --route alone appends to the router's current routes, and the two together
// replace them.
func buildRoutes(ctx context.Context, client *gophercloud.ServiceClient,
	nameOrID, id string, f *routerSetFlags,
) (*[]routers.Route, error) {
	if len(f.route) == 0 && !f.noRoute {
		return nil, nil
	}
	parsed, err := parseRoutes(f.route)
	if err != nil {
		return nil, err
	}
	// --no-route (with or without --route) makes the given list the whole list.
	if f.noRoute {
		if parsed == nil {
			parsed = []routers.Route{}
		}
		return &parsed, nil
	}
	current, err := routers.Get(ctx, client, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("reading router %s to append routes: %w", nameOrID, err)
	}
	combined := make([]routers.Route, 0, len(current.Routes)+len(parsed))
	combined = append(combined, current.Routes...)
	for _, candidate := range parsed {
		if !slices.Contains(combined, candidate) {
			combined = append(combined, candidate)
		}
	}
	return &combined, nil
}

// parseRoutes parses the repeatable --route specs into routers.Route values. Each
// spec is a comma-separated key=value list: destination=<cidr>,gateway=<ip>.
func parseRoutes(specs []string) ([]routers.Route, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]routers.Route, 0, len(specs))
	for _, spec := range specs {
		route, err := parseRouteSpec(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, nil
}

// parseRouteSpec parses one --route spec and validates that both halves of a
// static route are present. Neutron takes destination/nexthop; OSC spells the
// second one "gateway", so both are accepted and the alias is worth a test row
// of its own.
func parseRouteSpec(spec string) (routers.Route, error) {
	var route routers.Route
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, err := splitKV(part)
		if err != nil {
			return route, fmt.Errorf("parsing --route %q: %w", spec, err)
		}
		switch k {
		case "destination":
			route.DestinationCIDR = v
		case "gateway", "nexthop":
			route.NextHop = v
		default:
			return route, fmt.Errorf("parsing --route %q: unknown key %q", spec, k)
		}
	}
	if route.DestinationCIDR == "" || route.NextHop == "" {
		return route, fmt.Errorf("--route %q requires both destination and gateway", spec)
	}
	return route, nil
}

type routerUnsetFlags struct {
	externalGateway bool
	route           []string
	qosPolicy       bool
	extraProperty   []string
	tagWriteFlags
}

func newRouterUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &routerUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <router>",
		Short: "Unset router properties",
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
			return runRouterUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.externalGateway, flagRouterExternalGateway, false, "clear the router's external gateway")
	fl.StringArrayVar(&f.route, flagRouterRoute, nil,
		"static route to remove, as destination=<cidr>,gateway=<ip> (repeatable)")
	fl.BoolVar(&f.qosPolicy, flagQoSPolicy, false, "detach the gateway IPs' QoS policy")
	bindExtraPropertyUnsetFlag(fl, &f.extraProperty)
	bindTagUnsetFlags(cmd, &f.tagWriteFlags, "router")
	return cmd
}

// runRouterUnset mirrors upstream's UnsetRouter: --route rewrites the routes
// list minus the named ones (each must be present), --qos-policy re-sends the
// current gateway network with qos_policy_id null, --external-gateway clears
// the gateway (and so wins over --qos-policy), and --extra-property nulls the
// named attributes. The router is read only when --route or --qos-policy
// needs its current state.
func runRouterUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *routerUnsetFlags, w io.Writer) error {
	if !f.externalGateway && len(f.route) == 0 && !f.qosPolicy && len(f.extraProperty) == 0 && !f.given() {
		return fmt.Errorf("router unset requires at least one attribute flag")
	}
	id, err := resolveRouterID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts, attrs, err := buildRouterUnset(ctx, client, nameOrID, id, f)
	if err != nil {
		return err
	}
	action := "updating"
	if f.externalGateway {
		action = "clearing external gateway on"
	}
	u := routerUpdate{opts: opts, attrs: attrs, changed: opts != (routers.UpdateOpts{}) || len(attrs) > 0, action: action}
	return updateRouter(ctx, client, o, nameOrID, id, u, applyTagsForUnset, &f.tagWriteFlags, w)
}

func buildRouterUnset(ctx context.Context, client *gophercloud.ServiceClient,
	nameOrID, id string, f *routerUnsetFlags,
) (routers.UpdateOpts, map[string]any, error) {
	var (
		opts  routers.UpdateOpts
		attrs map[string]any
	)
	removed, err := parseRoutes(f.route)
	if err != nil {
		return opts, nil, err
	}
	if len(removed) > 0 || f.qosPolicy {
		current, err := routers.Get(ctx, client, id).Extract()
		if err != nil {
			return opts, nil, fmt.Errorf("reading router %s: %w", nameOrID, err)
		}
		if len(removed) > 0 {
			kept, err := routesWithout(current.Routes, removed, nameOrID)
			if err != nil {
				return opts, nil, err
			}
			opts.Routes = &kept
		}
		if f.qosPolicy {
			if current.GatewayInfo.NetworkID == "" {
				return opts, nil, fmt.Errorf("router %s has no external gateway, so no gateway QoS policy to unset", nameOrID)
			}
			// Exactly upstream's body: the network, and a null policy.
			attrs = map[string]any{"external_gateway_info": gatewayInfoBody(current.GatewayInfo.NetworkID, nil, nil, nil, true)}
		}
	}
	if f.externalGateway {
		// An empty object is neutron's "clear the external gateway".
		attrs = mergeAttrs(attrs, map[string]any{"external_gateway_info": map[string]any{}})
	}
	extra, err := parseExtraProperties(f.extraProperty, true)
	if err != nil {
		return opts, nil, err
	}
	return opts, mergeAttrs(attrs, extra), nil
}

// routesWithout returns have minus every route in remove, erroring on a route
// the router does not carry — upstream's "Router does not contain route".
func routesWithout(have, remove []routers.Route, nameOrID string) ([]routers.Route, error) {
	kept := slices.Clone(have)
	for _, r := range remove {
		i := slices.Index(kept, r)
		if i < 0 {
			return nil, fmt.Errorf("router %s does not contain route destination=%s,gateway=%s", nameOrID, r.DestinationCIDR, r.NextHop)
		}
		kept = slices.Delete(kept, i, i+1)
	}
	if kept == nil {
		kept = []routers.Route{}
	}
	return kept, nil
}

// newRouterAddCommand builds "router add subnet <router> <subnet>" and
// "router add port <router> <port>".
func newRouterAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a resource to a router",
	}
	cmd.AddCommand(newRouterAddSubnetCommand(a, o))
	cmd.AddCommand(&cobra.Command{
		Use:   "port <router> <port>",
		Short: "Add a port to a router (attach an internal interface)",
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
			return runRouterAddPort(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	})
	cmd.AddCommand(newRouterAddGatewayCommand(a, o))
	cmd.AddCommand(newRouterAddRouteCommand(a, o))
	return cmd
}

// newRouterAddSubnetCommand builds "router add subnet <router> <subnet>".
func newRouterAddSubnetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var advertiseHost bool
	cmd := &cobra.Command{
		Use:   "subnet <router> <subnet>",
		Short: "Add a subnet to a router (create an internal interface)",
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
			return runRouterAddSubnet(ctx, client, args[0], args[1], advertiseHost, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&advertiseHost, flagRouterAdvertiseHost, false,
		"advertise the subnet's prefixes as host routes within the router's EVPN VNI, EVPN routers only "+
			"(requires the evpn extension)")
	return cmd
}

// routerAddInterfaceExt adds top-level attributes to an add_router_interface
// body, which — unlike the router's own — is not wrapped in a resource key, so
// bodyExt does not fit. advertise_host (evpn) is the one such attribute.
type routerAddInterfaceExt struct {
	routers.AddInterfaceOpts
	extra map[string]any
}

// ToRouterAddInterfaceMap builds the typed body and merges extra over it.
func (e routerAddInterfaceExt) ToRouterAddInterfaceMap() (map[string]any, error) {
	body, err := e.AddInterfaceOpts.ToRouterAddInterfaceMap()
	if err != nil {
		return nil, err
	}
	return mergeAttrs(body, e.extra), nil
}

// runRouterAddSubnet attaches a subnet. As in openstacksdk's
// add_interface_to_router, advertise_host is sent only when true, so without
// --advertise-host the request a Zed cloud sees is unchanged.
func runRouterAddSubnet(ctx context.Context, client *gophercloud.ServiceClient, routerArg, subnetArg string,
	advertiseHost bool, w io.Writer,
) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	subnetID, err := resolveSubnetID(ctx, client, subnetArg)
	if err != nil {
		return err
	}
	var extra map[string]any
	if advertiseHost {
		extra = map[string]any{"advertise_host": true}
	}
	body := routerAddInterfaceExt{AddInterfaceOpts: routers.AddInterfaceOpts{SubnetID: subnetID}, extra: extra}
	if _, err := routers.AddInterface(ctx, client, routerID, body).Extract(); err != nil {
		return explainMissingExtension(ctx, client,
			fmt.Errorf("adding subnet %s to router %s: %w", subnetArg, routerArg, err), extra)
	}
	if _, err := fmt.Fprintf(w, "Added interface for subnet %s to router %s\n", subnetArg, routerArg); err != nil {
		return err
	}
	return nil
}

func runRouterAddPort(ctx context.Context, client *gophercloud.ServiceClient, routerArg, portArg string, w io.Writer) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	portID, err := resolvePortID(ctx, client, portArg)
	if err != nil {
		return err
	}
	if _, err := routers.AddInterface(ctx, client, routerID, routers.AddInterfaceOpts{PortID: portID}).Extract(); err != nil {
		return fmt.Errorf("adding port %s to router %s: %w", portArg, routerArg, err)
	}
	if _, err := fmt.Fprintf(w, "Added interface for port %s to router %s\n", portArg, routerArg); err != nil {
		return err
	}
	return nil
}

// newRouterRemoveCommand builds "router remove subnet <router> <subnet>" and
// "router remove port <router> <port>".
func newRouterRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a resource from a router",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "subnet <router> <subnet>",
		Short: "Remove a subnet from a router (delete an internal interface)",
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
			return runRouterRemoveSubnet(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "port <router> <port>",
		Short: "Remove a port from a router (detach an internal interface)",
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
			return runRouterRemovePort(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	})
	cmd.AddCommand(newRouterRemoveGatewayCommand(a, o))
	cmd.AddCommand(newRouterRemoveRouteCommand(a, o))
	return cmd
}

func runRouterRemoveSubnet(ctx context.Context, client *gophercloud.ServiceClient, routerArg, subnetArg string, w io.Writer) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	subnetID, err := resolveSubnetID(ctx, client, subnetArg)
	if err != nil {
		return err
	}
	if _, err := routers.RemoveInterface(ctx, client, routerID, routers.RemoveInterfaceOpts{SubnetID: subnetID}).Extract(); err != nil {
		return fmt.Errorf("removing subnet %s from router %s: %w", subnetArg, routerArg, err)
	}
	if _, err := fmt.Fprintf(w, "Removed interface for subnet %s from router %s\n", subnetArg, routerArg); err != nil {
		return err
	}
	return nil
}

func runRouterRemovePort(ctx context.Context, client *gophercloud.ServiceClient, routerArg, portArg string, w io.Writer) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	portID, err := resolvePortID(ctx, client, portArg)
	if err != nil {
		return err
	}
	if _, err := routers.RemoveInterface(ctx, client, routerID, routers.RemoveInterfaceOpts{PortID: portID}).Extract(); err != nil {
		return fmt.Errorf("removing port %s from router %s: %w", portArg, routerArg, err)
	}
	if _, err := fmt.Fprintf(w, "Removed interface for port %s from router %s\n", portArg, routerArg); err != nil {
		return err
	}
	return nil
}
