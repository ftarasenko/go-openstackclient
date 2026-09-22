package network

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/allprojects"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
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

// routerListFlags holds the filters accepted by "router list". Upstream OSC's
// parser (network/v2/router.py ListRouter) is the reference; --all-projects is
// the one koc-native addition — see allProjectsNetworkList.
type routerListFlags struct {
	name          string
	project       string
	projectDomain string
	enable        bool
	disable       bool
	tags          []string
	anyTags       []string
	notTags       []string
	notAnyTags    []string
	agent         string
	long          bool
	allProjects   bool

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
	fl.StringSliceVar(&f.tags, "tags", nil, "list only routers with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.anyTags, "any-tags", nil, "list only routers with any of these tags (comma-separated)")
	fl.StringSliceVar(&f.notTags, "not-tags", nil, "exclude routers with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.notAnyTags, "not-any-tags", nil, "exclude routers with any of these tags (comma-separated)")
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
type routerExtAttrs struct {
	Distributed       *bool     `json:"distributed"`
	HA                *bool     `json:"ha"`
	AvailabilityZones *[]string `json:"availability_zones"`
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
		Tags:         strings.Join(f.tags, ","),
		TagsAny:      strings.Join(f.anyTags, ","),
		NotTags:      strings.Join(f.notTags, ","),
		NotTagsAny:   strings.Join(f.notAnyTags, ","),
	}
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
	r, err := routers.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting router %s: %w", nameOrID, err)
	}
	interfaces, err := routerInterfaces(ctx, client, r.ID)
	if err != nil {
		return err
	}
	fields, values := routerShowFields(r)
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

type routerCreateFlags struct {
	enable  bool
	disable bool
}

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
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runRouterCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the router (admin state up, default)")
	fl.BoolVar(&f.disable, "disable", false, "disable the router (admin state down)")
	return cmd
}

func runRouterCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *routerCreateFlags, w io.Writer) error {
	opts := routers.CreateOpts{Name: name}
	if f.disable {
		opts.AdminStateUp = boolPtr(false)
	} else {
		opts.AdminStateUp = boolPtr(true)
	}
	r, err := routers.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating router: %w", err)
	}
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
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
	enable          bool
	disable         bool
	externalGateway string

	enableSNAT  bool
	disableSNAT bool
	qosPolicy   string
	route       []string
	noRoute     bool
}

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
	fl.BoolVar(&f.enable, "enable", false, "enable the router (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "disable the router (admin state down)")
	fl.StringVar(&f.externalGateway, "external-gateway", "", "set the external gateway network (name or ID)")
	fl.BoolVar(&f.enableSNAT, flagEnableSNAT, false, "enable source NAT on the external gateway")
	fl.BoolVar(&f.disableSNAT, flagDisableSNAT, false, "disable source NAT on the external gateway")
	fl.StringVar(&f.qosPolicy, "qos-policy", "", "QoS policy ID to attach to the external gateway")
	fl.StringArrayVar(&f.route, "route", nil,
		"static route as destination=<cidr>,gateway=<ip> (repeatable; appends unless --no-route is also given)")
	fl.BoolVar(&f.noRoute, "no-route", false, "clear the router's static routes (with --route, replaces them instead)")
	cmd.MarkFlagsMutuallyExclusive(flagEnableSNAT, flagDisableSNAT)
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
	opts := routers.UpdateOpts{}
	changed := false
	if f.name != "" {
		opts.Name = f.name
		changed = true
	}
	if state := enableDisable(flags, f.enable, f.disable); state != nil {
		opts.AdminStateUp = state
		changed = true
	}
	gateway, err := buildGatewayInfo(ctx, client, nameOrID, id, f, flags)
	if err != nil {
		return err
	}
	if gateway != nil {
		opts.GatewayInfo = gateway
		changed = true
	}
	routes, err := buildRoutes(ctx, client, nameOrID, id, f)
	if err != nil {
		return err
	}
	if routes != nil {
		opts.Routes = routes
		changed = true
	}
	if !changed {
		return fmt.Errorf("router set requires at least one attribute flag")
	}
	r, err := routers.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating router %s: %w", nameOrID, err)
	}
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
}

// buildGatewayInfo assembles external_gateway_info from --external-gateway,
// --enable-snat/--disable-snat and --qos-policy, returning nil when none were
// given.
//
// Neutron replaces external_gateway_info wholesale and rejects it without a
// network_id, so changing only SNAT or the QoS policy means re-sending the
// router's current gateway network — which is read back here rather than
// requiring the operator to repeat --external-gateway.
func buildGatewayInfo(ctx context.Context, client *gophercloud.ServiceClient,
	nameOrID, id string, f *routerSetFlags, flags flagSet,
) (*routers.GatewayInfo, error) {
	snat := enableDisable(flags, f.enableSNAT, f.disableSNAT, flagEnableSNAT, flagDisableSNAT)
	if f.externalGateway == "" && snat == nil && f.qosPolicy == "" {
		return nil, nil
	}

	info := &routers.GatewayInfo{EnableSNAT: snat, QoSPolicyID: f.qosPolicy}
	if f.externalGateway != "" {
		gwID, err := resolveNetworkID(ctx, client, f.externalGateway)
		if err != nil {
			return nil, err
		}
		info.NetworkID = gwID
		return info, nil
	}

	current, err := routers.Get(ctx, client, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("reading router %s to preserve its external gateway: %w", nameOrID, err)
	}
	if current.GatewayInfo.NetworkID == "" {
		return nil, fmt.Errorf("router %s has no external gateway: pass --external-gateway alongside --enable-snat/--disable-snat/--qos-policy", nameOrID)
	}
	info.NetworkID = current.GatewayInfo.NetworkID
	// Keep the fixed IPs neutron already assigned; omitting them would make it
	// reallocate from the external subnet, changing the router's gateway address.
	info.ExternalFixedIPs = current.GatewayInfo.ExternalFixedIPs
	if info.EnableSNAT == nil {
		info.EnableSNAT = current.GatewayInfo.EnableSNAT
	}
	if info.QoSPolicyID == "" {
		info.QoSPolicyID = current.GatewayInfo.QoSPolicyID
	}
	return info, nil
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

func newRouterUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var externalGateway bool
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
			return runRouterUnset(ctx, client, o, args[0], externalGateway, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&externalGateway, "external-gateway", false, "clear the router's external gateway")
	return cmd
}

func runRouterUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, externalGateway bool, w io.Writer) error {
	if !externalGateway {
		return fmt.Errorf("router unset requires --external-gateway")
	}
	id, err := resolveRouterID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	// An empty GatewayInfo object serializes to "external_gateway_info": {},
	// which neutron interprets as clearing the external gateway.
	opts := routers.UpdateOpts{GatewayInfo: &routers.GatewayInfo{}}
	r, err := routers.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("clearing external gateway on router %s: %w", nameOrID, err)
	}
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
}

// newRouterAddCommand builds "router add subnet <router> <subnet>" and
// "router add port <router> <port>".
func newRouterAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a resource to a router",
	}
	cmd.AddCommand(&cobra.Command{
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
			return runRouterAddSubnet(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	})
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

func runRouterAddSubnet(ctx context.Context, client *gophercloud.ServiceClient, routerArg, subnetArg string, w io.Writer) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	subnetID, err := resolveSubnetID(ctx, client, subnetArg)
	if err != nil {
		return err
	}
	if _, err := routers.AddInterface(ctx, client, routerID, routers.AddInterfaceOpts{SubnetID: subnetID}).Extract(); err != nil {
		return fmt.Errorf("adding subnet %s to router %s: %w", subnetArg, routerArg, err)
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
