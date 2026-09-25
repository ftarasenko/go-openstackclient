package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/segments"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names the subnet verbs look up as well as declare. They carry a subnet
// prefix so they cannot collide with a sibling noun's constants.
const (
	subnetFlagAllocationPool      = "allocation-pool"
	subnetFlagNoAllocationPool    = "no-allocation-pool"
	subnetFlagHostRoute           = "host-route"
	subnetFlagNoHostRoute         = "no-host-route"
	subnetFlagServiceType         = "service-type"
	subnetFlagNoDNSNameservers    = "no-dns-nameservers"
	subnetFlagNetworkSegment      = "network-segment"
	subnetFlagDNSPublishFixedIP   = "dns-publish-fixed-ip"
	subnetFlagNoDNSPublishFixedIP = "no-dns-publish-fixed-ip"
	subnetFlagSubnetPool          = "subnet-pool"
	subnetFlagUsePrefixDelegation = "use-prefix-delegation"
	subnetFlagUseDefaultPool      = "use-default-subnet-pool"
	subnetFlagGateway             = "gateway"
	subnetFlagNoGateway           = "no-gateway"
	subnetFlagName                = "name"
	subnetFlagLeakRoutes          = "leak-routes"
	subnetFlagNoLeakRoutes        = "no-leak-routes"

	subnetAttrLeakRoutes      = "leak_routes"
	subnetAttrDNSPublishFixed = "dns_publish_fixed_ip"
	subnetAttrServiceTypes    = "service_types"
)

// SubnetLeakExt carries ovn-bgp's leak_routes, which subnets.Subnet does not
// model. A pointer, so a cloud without the extension renders an empty cell
// rather than "false". Exported for the same reason as MTUExt.
type SubnetLeakExt struct {
	LeakRoutes *bool `json:"leak_routes"`
}

// subnetExt is a Subnet decorated with the extension attributes koc renders.
type subnetExt struct {
	subnets.Subnet
	SubnetLeakExt
}

// extractSubnet decodes a subnet GET/POST/PUT result into a subnetExt.
func extractSubnet(r interface {
	ExtractIntoStructPtr(to any, label string) error
},
) (*subnetExt, error) {
	var s subnetExt
	if err := r.ExtractIntoStructPtr(&s, "subnet"); err != nil {
		return nil, err
	}
	return &s, nil
}

// withExplainKeys returns attrs plus the named attributes the typed opts carry,
// for explainMissingExtension, which only looks at which keys a body sent.
func withExplainKeys(attrs map[string]any, keys ...string) map[string]any {
	if len(keys) == 0 {
		return attrs
	}
	out := make(map[string]any, len(attrs)+len(keys))
	for k, v := range attrs {
		out[k] = v
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			out[k] = true
		}
	}
	return out
}

// subnetTypedExtKeys names the extension attributes a subnet request carries
// in its typed opts rather than in the attrs map.
func subnetTypedExtKeys(dnsPublish *bool, serviceTypes bool) []string {
	var keys []string
	if dnsPublish != nil {
		keys = append(keys, subnetAttrDNSPublishFixed)
	}
	if serviceTypes {
		keys = append(keys, subnetAttrServiceTypes)
	}
	return keys
}

func newSubnetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subnet",
		Short: "Manage subnets",
	}
	cmd.AddCommand(newSubnetListCommand(a, o))
	cmd.AddCommand(newSubnetShowCommand(a, o))
	cmd.AddCommand(newSubnetCreateCommand(a, o))
	cmd.AddCommand(newSubnetDeleteCommand(a, o))
	cmd.AddCommand(newSubnetSetCommand(a, o))
	cmd.AddCommand(newSubnetUnsetCommand(a, o))
	cmd.AddCommand(newSubnetPoolCommand(a, o))
	return cmd
}

func subnetShowFields(s *subnetExt) ([]string, []any) {
	fields := []string{
		"id", "name", "network_id", "cidr", "ip_version", "gateway_ip",
		"enable_dhcp", "dns_nameservers", subnetAttrDNSPublishFixed, "allocation_pools",
		"host_routes", subnetAttrServiceTypes, "ipv6_address_mode", "ipv6_ra_mode",
		"subnetpool_id", "segment_id", subnetAttrLeakRoutes, "description", "project_id", "tags",
		"revision_number", "created_at", "updated_at",
	}
	values := []any{
		s.ID, s.Name, s.NetworkID, s.CIDR, s.IPVersion, s.GatewayIP,
		s.EnableDHCP, s.DNSNameservers, s.DNSPublishFixedIP, s.AllocationPools,
		s.HostRoutes, s.ServiceTypes, s.IPv6AddressMode, s.IPv6RAMode,
		s.SubnetPoolID, s.SegmentID, s.LeakRoutes, s.Description, s.ProjectID, s.Tags,
		s.RevisionNumber, s.CreatedAt, s.UpdatedAt,
	}
	return fields, values
}

// resolveSubnetSegmentID resolves a network segment name or ID for
// --network-segment. A UUID passes straight through; anything else is looked
// up by name under the shared name-or-ID policy (helpers.go pickID).
func resolveSubnetSegmentID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	if resolve.IsUUID(nameOrID) {
		return nameOrID, nil
	}
	return resolveByName(client, "network segment", nameOrID, func(c *gophercloud.ServiceClient) ([]segments.Segment, error) {
		pages, err := segments.List(c, segments.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return segments.ExtractSegments(pages)
	}, func(s segments.Segment) string { return s.ID })
}

// subnetListFlags holds the filters accepted by "subnet list".
type subnetListFlags struct {
	name          string
	network       string
	project       string
	projectDomain string
	subnetPool    string
	subnetRange   string
	serviceTypes  []string
	gateway       string
	ipVersion     int
	dhcp          bool
	noDHCP        bool
	long          bool
	tagFilterFlags

	enableDHCP *bool
}

func newSubnetListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subnets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, flagDHCP, flagNoDHCP); err != nil {
				return err
			}
			f.enableDHCP = enableDisable(fl, f.dhcp, f.noDHCP, flagDHCP, flagNoDHCP)
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runSubnetList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, subnetFlagName, "", "list subnets matching this name")
	fl.StringVar(&f.network, "network", "", "list subnets of this network (name or ID)")
	fl.StringVar(&f.project, flagProject, "", "list subnets owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.StringVar(&f.subnetPool, subnetFlagSubnetPool, "", "list subnets allocated from this subnet pool (name or ID)")
	fl.StringVar(&f.subnetRange, "subnet-range", "", "list subnets with this CIDR range (e.g. 10.10.0.0/16)")
	fl.StringArrayVar(&f.serviceTypes, subnetFlagServiceType, nil,
		"list subnets with this service type, e.g. network:floatingip_agent_gateway (repeatable)")
	fl.StringVar(&f.gateway, subnetFlagGateway, "", "list subnets with this gateway IP")
	fl.IntVar(&f.ipVersion, "ip-version", 0, "list subnets of this IP version (4 or 6)")
	fl.BoolVar(&f.dhcp, flagDHCP, false, "list only subnets with DHCP enabled")
	fl.BoolVar(&f.noDHCP, flagNoDHCP, false, "list only subnets with DHCP disabled")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	bindTagFilterFlags(fl, &f.tagFilterFlags, "subnets")
	return cmd
}

// subnetListOpts adds service_types, which ListOpts does not model and which
// upstream sends once per --service-type.
type subnetListOpts struct {
	subnets.ListOpts
	extra url.Values
}

func (opts subnetListOpts) ToSubnetListQuery() (string, error) {
	q, err := opts.ListOpts.ToSubnetListQuery()
	return withQueryValues(q, err, opts.extra)
}

func runSubnetList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *subnetListFlags, projectID string, w io.Writer,
) error {
	opts := subnetListOpts{
		ListOpts: subnets.ListOpts{
			Name:       f.name,
			ProjectID:  projectID,
			GatewayIP:  f.gateway,
			IPVersion:  f.ipVersion,
			EnableDHCP: f.enableDHCP,
			CIDR:       f.subnetRange,
		},
		extra: url.Values{},
	}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
	if len(f.serviceTypes) > 0 {
		opts.extra["service_types"] = f.serviceTypes
	}
	if f.network != "" {
		networkID, err := resolveNetworkID(ctx, client, f.network)
		if err != nil {
			return err
		}
		opts.NetworkID = networkID
	}
	if f.subnetPool != "" {
		poolID, err := resolveSubnetPoolID(ctx, client, f.subnetPool)
		if err != nil {
			return err
		}
		opts.SubnetPoolID = poolID
	}
	pages, err := subnets.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing subnets: %w", err)
	}
	all, err := subnets.ExtractSubnets(pages)
	if err != nil {
		return fmt.Errorf("parsing subnet list: %w", err)
	}
	return o.WriteList(w, subnetListTable(all, f.long))
}

// subnetListTable renders upstream's columns. --long adds upstream's nine, then
// koc's "Subnet Pool", kept at the end for scripts that already select it.
func subnetListTable(all []subnets.Subnet, long bool) output.Table {
	cols := []string{"ID", "Name", "Network", "Subnet"}
	if long {
		cols = append(cols, "Project", "DHCP", "Name Servers", "Allocation Pools", "Host Routes",
			"IP Version", "Gateway", "Service Types", "Tags", "Subnet Pool")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		row := []any{s.ID, s.Name, s.NetworkID, s.CIDR}
		if long {
			row = append(row, s.ProjectID, s.EnableDHCP, s.DNSNameservers,
				formatAllocationPools(s.AllocationPools), formatHostRoutes(s.HostRoutes),
				s.IPVersion, s.GatewayIP, s.ServiceTypes, s.Tags, s.SubnetPoolID)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// formatAllocationPools renders a subnet's allocation pools as "start-end"
// entries so the range fits one table cell.
func formatAllocationPools(pools []subnets.AllocationPool) []string {
	out := make([]string, 0, len(pools))
	for _, p := range pools {
		out = append(out, p.Start+"-"+p.End)
	}
	return out
}

// formatHostRoutes renders host routes in the --host-route spelling
// (gateway=, not the API's nexthop=), as upstream's HostRoutesColumn does.
func formatHostRoutes(routes []subnets.HostRoute) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, "destination="+r.DestinationCIDR+",gateway="+r.NextHop)
	}
	return out
}

func newSubnetShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <subnet>",
		Short: "Show details of a subnet",
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
			return runSubnetShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSubnetShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveSubnetID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	s, err := extractSubnet(subnets.Get(ctx, client, id))
	if err != nil {
		return fmt.Errorf("getting subnet %s: %w", nameOrID, err)
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

// subnetCreateFlags mirrors upstream CreateSubnet (network/v2/subnet.py).
// --subnet-range is optional when the CIDR comes from a pool (--subnet-pool,
// --use-default-subnet-pool, --use-prefix-delegation); neutron enforces it
// otherwise. --ip-version is always sent (default 4), as upstream does; with a
// pool neutron takes the version from the pool.
type subnetCreateFlags struct {
	network              string
	subnetRange          string
	ipVersion            int
	gateway              string
	dhcp                 bool
	noDHCP               bool
	dnsNameservers       []string
	allocationPool       []string
	hostRoute            []string
	serviceType          []string
	description          string
	subnetPool           string
	useDefaultSubnetPool bool
	usePrefixDelegation  bool
	prefixLength         int
	ipv6RAMode           string
	ipv6AddressMode      string
	networkSegment       string
	dnsPublishFixedIP    bool
	noDNSPublishFixedIP  bool
	project              string
	projectDomain        string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	extraProperty []string
	tagWriteFlags
}

func newSubnetCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new subnet",
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
			return runSubnetCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.network, "network", "", "network this subnet belongs to (name or ID, required)")
	fl.StringVar(&f.subnetRange, "subnet-range", "",
		"subnet CIDR range, e.g. 10.0.0.0/24 (required unless the CIDR comes from a subnet pool)")
	fl.IntVar(&f.ipVersion, "ip-version", 4, "IP version: 4 or 6 (taken from the pool when one is used)")
	fl.StringVar(&f.gateway, subnetFlagGateway, "",
		"gateway: an IP address, 'auto' (default: chosen from the subnet) or 'none' (no gateway)")
	fl.BoolVar(&f.dhcp, flagDHCP, false, "enable DHCP (default)")
	fl.BoolVar(&f.noDHCP, flagNoDHCP, false, "disable DHCP")
	fl.StringArrayVar(&f.dnsNameservers, flagDNSNameserver, nil, "DNS nameserver (repeatable)")
	fl.StringArrayVar(&f.allocationPool, subnetFlagAllocationPool, nil, "allocation pool as start=<ip>,end=<ip> (repeatable)")
	fl.StringArrayVar(&f.hostRoute, subnetFlagHostRoute, nil, "host route as destination=<cidr>,gateway=<ip> (repeatable)")
	fl.StringArrayVar(&f.serviceType, subnetFlagServiceType, nil,
		"service type, a port device owner such as network:floatingip_agent_gateway (repeatable)")
	fl.StringVar(&f.description, flagDescription, "", "description for the subnet")
	fl.StringVar(&f.subnetPool, subnetFlagSubnetPool, "", "subnet pool to allocate the CIDR from (name or ID)")
	fl.BoolVar(&f.useDefaultSubnetPool, subnetFlagUseDefaultPool, false, "allocate the CIDR from the default subnet pool for --ip-version")
	fl.BoolVar(&f.usePrefixDelegation, subnetFlagUsePrefixDelegation, false, "obtain the IPv6 prefix by external prefix delegation")
	fl.IntVar(&f.prefixLength, "prefix-length", 0, "prefix length of the CIDR allocated from the subnet pool")
	fl.StringVar(&f.ipv6RAMode, "ipv6-ra-mode", "", "IPv6 router advertisement mode: dhcpv6-stateful, dhcpv6-stateless or slaac")
	fl.StringVar(&f.ipv6AddressMode, "ipv6-address-mode", "", "IPv6 address mode: dhcpv6-stateful, dhcpv6-stateless or slaac")
	fl.StringVar(&f.networkSegment, subnetFlagNetworkSegment, "", "network segment to associate with the subnet (name or ID)")
	fl.BoolVar(&f.dnsPublishFixedIP, subnetFlagDNSPublishFixedIP, false, "publish fixed IPs in DNS")
	fl.BoolVar(&f.noDNSPublishFixedIP, subnetFlagNoDNSPublishFixedIP, false, "do not publish fixed IPs in DNS (default)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagCreateFlags(cmd, &f.tagWriteFlags, "subnet")
	_ = cmd.MarkFlagRequired("network")
	cmd.MarkFlagsMutuallyExclusive(subnetFlagSubnetPool, subnetFlagUsePrefixDelegation, subnetFlagUseDefaultPool)
	cmd.MarkFlagsMutuallyExclusive(flagDHCP, flagNoDHCP)
	cmd.MarkFlagsMutuallyExclusive(subnetFlagDNSPublishFixedIP, subnetFlagNoDNSPublishFixedIP)
	return cmd
}

func runSubnetCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *subnetCreateFlags, w io.Writer) error {
	opts, attrs, err := buildSubnetCreateOpts(ctx, client, name, f)
	if err != nil {
		return err
	}
	s, err := extractSubnet(subnets.Create(ctx, client, withSubnetCreateAttrs(opts, attrs)))
	if err != nil {
		explain := withExplainKeys(attrs, subnetTypedExtKeys(opts.DNSPublishFixedIP, len(opts.ServiceTypes) > 0)...)
		return explainMissingExtension(ctx, client, fmt.Errorf("creating subnet: %w", err), explain)
	}
	// Tags cannot ride on the create; upstream sets them afterwards too.
	if s.Tags, err = applyTagsForSet(ctx, client, tagResourceSubnets, s.ID, s.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

// buildSubnetCreateOpts turns the create flags into the typed opts plus the
// attributes gophercloud does not model (use_default_subnetpool and
// --extra-property, the latter last so it wins).
func buildSubnetCreateOpts(ctx context.Context, client *gophercloud.ServiceClient, name string, f *subnetCreateFlags) (subnets.CreateOpts, map[string]any, error) {
	opts, err := subnetCreateBaseOpts(name, f)
	if err != nil {
		return opts, nil, err
	}
	if opts.NetworkID, err = resolveNetworkID(ctx, client, f.network); err != nil {
		return opts, nil, err
	}
	switch {
	case f.subnetPool != "":
		if opts.SubnetPoolID, err = resolveSubnetPoolID(ctx, client, f.subnetPool); err != nil {
			return opts, nil, err
		}
	case f.usePrefixDelegation:
		// Neutron's reserved pool ID for IPv6 prefix delegation.
		opts.SubnetPoolID = "prefix_delegation"
	}
	if f.networkSegment != "" {
		if opts.SegmentID, err = resolveSubnetSegmentID(ctx, client, f.networkSegment); err != nil {
			return opts, nil, err
		}
	}
	var attrs map[string]any
	if f.useDefaultSubnetPool {
		attrs = map[string]any{"use_default_subnetpool": true}
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return opts, nil, err
	}
	return opts, mergeAttrs(attrs, extra), nil
}

// subnetCreateBaseOpts fills every create attribute that needs no lookup.
func subnetCreateBaseOpts(name string, f *subnetCreateFlags) (subnets.CreateOpts, error) {
	opts := subnets.CreateOpts{
		Name:           name,
		CIDR:           f.subnetRange,
		IPVersion:      gophercloud.IPVersion(f.ipVersion),
		DNSNameservers: f.dnsNameservers,
		ServiceTypes:   f.serviceType,
		Description:    f.description,
		ProjectID:      f.projectID,
		Prefixlen:      f.prefixLength,
	}
	for _, mode := range []struct{ flag, value string }{
		{"ipv6-ra-mode", f.ipv6RAMode}, {"ipv6-address-mode", f.ipv6AddressMode},
	} {
		if mode.value != "" && !slices.Contains([]string{"dhcpv6-stateful", "dhcpv6-stateless", "slaac"}, mode.value) {
			return opts, fmt.Errorf("--%s must be dhcpv6-stateful, dhcpv6-stateless or slaac, got %q", mode.flag, mode.value)
		}
	}
	opts.IPv6RAMode = f.ipv6RAMode
	opts.IPv6AddressMode = f.ipv6AddressMode
	// "auto" (and no --gateway) leaves the choice to neutron; "none" is an
	// explicit null, which ToSubnetCreateMap derives from the empty string.
	switch gw := strings.ToLower(f.gateway); gw {
	case "", "auto":
	case "none":
		empty := ""
		opts.GatewayIP = &empty
	default:
		opts.GatewayIP = &gw
	}
	switch {
	case f.noDHCP:
		opts.EnableDHCP = boolPtr(false)
	case f.dhcp:
		opts.EnableDHCP = boolPtr(true)
	}
	switch {
	case f.noDNSPublishFixedIP:
		opts.DNSPublishFixedIP = boolPtr(false)
	case f.dnsPublishFixedIP:
		opts.DNSPublishFixedIP = boolPtr(true)
	}
	var err error
	if len(f.allocationPool) > 0 {
		if opts.AllocationPools, err = parseAllocationPools(f.allocationPool); err != nil {
			return opts, err
		}
	}
	if len(f.hostRoute) > 0 {
		if opts.HostRoutes, err = parseHostRoutes(f.hostRoute); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func newSubnetDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <subnet> [<subnet> ...]",
		Short: "Delete subnet(s)",
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
			return runSubnetDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSubnetDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	return batchdelete.Each(names, func(nameOrID string) error {
		id, err := resolveSubnetID(ctx, client, nameOrID)
		if err != nil {
			return err
		}
		if err := subnets.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting subnet %s: %w", nameOrID, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted subnet %s\n", nameOrID); err != nil {
			return err
		}
		return nil
	})
}

// subnetSetFlags mirrors upstream SetSubnet. The list flags --dns-nameserver,
// --allocation-pool, --host-route and --service-type ADD to what the subnet
// already carries; their --no-* partner clears the list first, so giving both
// overwrites it (--service-type has no --no-* partner upstream).
// --leak-routes/--no-leak-routes need the ovn-bgp extension, newer than Zed;
// they are set-only, as upstream (allow_post is false in neutron-lib's ovn_bgp).
type subnetSetFlags struct {
	name                string
	description         string
	dnsNameservers      []string
	noDNSNameservers    bool
	allocationPool      []string
	noAllocationPool    bool
	hostRoute           []string
	noHostRoute         bool
	serviceType         []string
	gateway             string
	noGateway           bool
	dhcp                bool
	noDHCP              bool
	dnsPublishFixedIP   bool
	noDNSPublishFixedIP bool
	leakRoutes          bool
	noLeakRoutes        bool
	networkSegment      string
	extraProperty       []string
	tagWriteFlags
}

// needsCurrent reports whether a list flag appends to the subnet's current
// value, which then has to be read first.
func (f *subnetSetFlags) needsCurrent() bool {
	return (len(f.dnsNameservers) > 0 && !f.noDNSNameservers) ||
		(len(f.allocationPool) > 0 && !f.noAllocationPool) ||
		(len(f.hostRoute) > 0 && !f.noHostRoute) ||
		len(f.serviceType) > 0
}

func newSubnetSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <subnet>",
		Short: "Set subnet properties",
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
			return runSubnetSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, subnetFlagName, "", "new subnet name")
	fl.StringVar(&f.description, flagDescription, "", "new description for the subnet")
	fl.StringArrayVar(&f.dnsNameservers, flagDNSNameserver, nil,
		"DNS nameserver to add (repeatable; with --no-dns-nameservers, replaces the list)")
	fl.BoolVar(&f.noDNSNameservers, subnetFlagNoDNSNameservers, false,
		"clear the DNS nameservers; combine with --dns-nameserver to overwrite them")
	fl.StringArrayVar(&f.allocationPool, subnetFlagAllocationPool, nil,
		"allocation pool to add as start=<ip>,end=<ip> (repeatable; with --no-allocation-pool, replaces the list)")
	fl.BoolVar(&f.noAllocationPool, subnetFlagNoAllocationPool, false,
		"clear the allocation pools; combine with --allocation-pool to overwrite them")
	fl.StringArrayVar(&f.hostRoute, subnetFlagHostRoute, nil,
		"host route to add as destination=<cidr>,gateway=<ip> (repeatable; with --no-host-route, replaces the list)")
	fl.BoolVar(&f.noHostRoute, subnetFlagNoHostRoute, false,
		"clear the host routes; combine with --host-route to overwrite them")
	fl.StringArrayVar(&f.serviceType, subnetFlagServiceType, nil, "service type to add (repeatable)")
	fl.StringVar(&f.gateway, subnetFlagGateway, "", "gateway IP address, or 'none' to clear it")
	fl.BoolVar(&f.noGateway, subnetFlagNoGateway, false, "clear the subnet gateway IP (same as --gateway none)")
	fl.BoolVar(&f.dhcp, flagDHCP, false, "enable DHCP")
	fl.BoolVar(&f.noDHCP, flagNoDHCP, false, "disable DHCP")
	fl.BoolVar(&f.dnsPublishFixedIP, subnetFlagDNSPublishFixedIP, false, "publish fixed IPs in DNS")
	fl.BoolVar(&f.noDNSPublishFixedIP, subnetFlagNoDNSPublishFixedIP, false, "do not publish fixed IPs in DNS")
	fl.BoolVar(&f.leakRoutes, subnetFlagLeakRoutes, false,
		"leak the subnet's routes to the underlay BGP fabric (requires the ovn-bgp extension)")
	fl.BoolVar(&f.noLeakRoutes, subnetFlagNoLeakRoutes, false,
		"do not leak the subnet's routes to the underlay BGP fabric (requires the ovn-bgp extension)")
	fl.StringVar(&f.networkSegment, subnetFlagNetworkSegment, "",
		"network segment to associate (name or ID; only while the subnet has none)")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagSetFlags(fl, &f.tagWriteFlags, "subnet")
	cmd.MarkFlagsMutuallyExclusive(flagDHCP, flagNoDHCP)
	cmd.MarkFlagsMutuallyExclusive(subnetFlagGateway, subnetFlagNoGateway)
	cmd.MarkFlagsMutuallyExclusive(subnetFlagDNSPublishFixedIP, subnetFlagNoDNSPublishFixedIP)
	cmd.MarkFlagsMutuallyExclusive(subnetFlagLeakRoutes, subnetFlagNoLeakRoutes)
	return cmd
}

func runSubnetSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *subnetSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolveSubnetID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	var (
		current *subnetExt
		base    *subnets.Subnet
	)
	if f.needsCurrent() {
		if current, err = extractSubnet(subnets.Get(ctx, client, id)); err != nil {
			return fmt.Errorf("reading subnet %s before set: %w", nameOrID, err)
		}
		base = &current.Subnet
	}
	opts, attrs, changed, err := buildSubnetSetUpdate(ctx, client, f, flags, base)
	if err != nil {
		return err
	}
	if !changed && !f.given() {
		return fmt.Errorf("subnet set requires at least one attribute flag")
	}
	var s *subnetExt
	switch {
	case changed:
		if current != nil {
			// The appended lists were computed from this read; pin the update to
			// it (neutron's If-Match) so a concurrent change is not clobbered.
			revision := current.RevisionNumber
			opts.RevisionNumber = &revision
		}
		if s, err = extractSubnet(subnets.Update(ctx, client, id, withSubnetUpdateAttrs(opts, attrs))); err != nil {
			explain := withExplainKeys(attrs, subnetTypedExtKeys(opts.DNSPublishFixedIP, opts.ServiceTypes != nil)...)
			return explainMissingExtension(ctx, client, fmt.Errorf("updating subnet %s: %w", nameOrID, err), explain)
		}
	case current != nil:
		s = current
	default:
		// Tags are the only change: upstream skips the PUT, so read the subnet
		// for its current tags instead.
		if s, err = extractSubnet(subnets.Get(ctx, client, id)); err != nil {
			return fmt.Errorf("getting subnet %s: %w", nameOrID, err)
		}
	}
	if s.Tags, err = applyTagsForSet(ctx, client, tagResourceSubnets, id, s.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

// buildSubnetSetUpdate collects every attribute the set flags change. changed
// is false when nothing but tags was given. current is the subnet as read,
// non-nil exactly when a list flag appends to it.
func buildSubnetSetUpdate(ctx context.Context, client *gophercloud.ServiceClient, f *subnetSetFlags, flags flagSet,
	current *subnets.Subnet,
) (subnets.UpdateOpts, map[string]any, bool, error) {
	opts := subnets.UpdateOpts{}
	changed, err := subnetSetScalars(f, flags, &opts)
	if err != nil {
		return opts, nil, false, err
	}
	attrs, listsChanged, err := subnetSetLists(f, current, &opts)
	if err != nil {
		return opts, nil, false, err
	}
	changed = changed || listsChanged
	if f.networkSegment != "" {
		segID, err := resolveSubnetSegmentID(ctx, client, f.networkSegment)
		if err != nil {
			return opts, nil, false, err
		}
		opts.SegmentID = &segID
		changed = true
	}
	attrs = setOptional(attrs, subnetAttrLeakRoutes, pairBool(f.leakRoutes, f.noLeakRoutes))
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return opts, nil, false, err
	}
	attrs = mergeAttrs(attrs, extra)
	return opts, attrs, changed || len(attrs) > 0, nil
}

// subnetSetScalars sets the single-valued attributes and reports whether any
// was given.
func subnetSetScalars(f *subnetSetFlags, flags flagSet, opts *subnets.UpdateOpts) (bool, error) {
	changed := false
	if flags.Changed(subnetFlagName) {
		opts.Name = &f.name
		changed = true
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
		changed = true
	}
	gateway := strings.ToLower(f.gateway)
	switch {
	case f.noGateway || gateway == "none":
		// A pointer to "" becomes an explicit null in ToSubnetUpdateMap, which is
		// how neutron drops the gateway.
		empty := ""
		opts.GatewayIP = &empty
		changed = true
	case gateway == "auto":
		return false, fmt.Errorf("--gateway auto is not available on subnet set: give an IP address or none")
	case gateway != "":
		opts.GatewayIP = &gateway
		changed = true
	}
	switch {
	case f.noDHCP:
		opts.EnableDHCP = boolPtr(false)
		changed = true
	case f.dhcp:
		opts.EnableDHCP = boolPtr(true)
		changed = true
	}
	switch {
	case f.noDNSPublishFixedIP:
		opts.DNSPublishFixedIP = boolPtr(false)
		changed = true
	case f.dnsPublishFixedIP:
		opts.DNSPublishFixedIP = boolPtr(true)
		changed = true
	}
	return changed, nil
}

// subnetSetLists applies upstream's append-unless-cleared rule to the four
// list attributes. An emptied allocation-pool list is returned as a body
// attribute: UpdateOpts tags AllocationPools omitempty, so an empty slice
// there would not be sent at all.
func subnetSetLists(f *subnetSetFlags, current *subnets.Subnet, opts *subnets.UpdateOpts) (map[string]any, bool, error) {
	if current == nil {
		current = &subnets.Subnet{}
	}
	changed := false
	if len(f.dnsNameservers) > 0 || f.noDNSNameservers {
		list := appendUnlessCleared(f.dnsNameservers, f.noDNSNameservers, current.DNSNameservers)
		opts.DNSNameservers = &list
		changed = true
	}
	if len(f.hostRoute) > 0 || f.noHostRoute {
		given, err := parseHostRoutes(f.hostRoute)
		if err != nil {
			return nil, false, err
		}
		list := appendUnlessCleared(given, f.noHostRoute, current.HostRoutes)
		opts.HostRoutes = &list
		changed = true
	}
	if len(f.serviceType) > 0 {
		list := appendUnlessCleared(f.serviceType, false, current.ServiceTypes)
		opts.ServiceTypes = &list
		changed = true
	}
	var attrs map[string]any
	if len(f.allocationPool) > 0 || f.noAllocationPool {
		given, err := parseAllocationPools(f.allocationPool)
		if err != nil {
			return nil, false, err
		}
		list := appendUnlessCleared(given, f.noAllocationPool, current.AllocationPools)
		if len(list) == 0 {
			attrs = map[string]any{"allocation_pools": []subnets.AllocationPool{}}
		} else {
			opts.AllocationPools = list
		}
		changed = true
	}
	return attrs, changed, nil
}

// appendUnlessCleared is upstream's "attrs[x] += obj.x unless --no-x": the
// given entries first, then the current ones unless the list is being cleared.
// The result is never nil, so a cleared list is sent as [].
func appendUnlessCleared[T any](given []T, cleared bool, current []T) []T {
	out := make([]T, 0, len(given)+len(current))
	out = append(out, given...)
	if !cleared {
		out = append(out, current...)
	}
	return out
}
