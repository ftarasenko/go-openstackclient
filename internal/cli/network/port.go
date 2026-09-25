package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/allprojects"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names only the port verbs use. They carry a port prefix so they cannot
// collide with a constant another noun declares in this package.
const (
	flagPortHost             = "host"
	flagPortDevice           = "device"
	flagPortVNICType         = "vnic-type"
	flagPortBindingProfile   = "binding-profile"
	flagPortNoBindingProfile = "no-binding-profile"
	flagPortNoFixedIP        = "no-fixed-ip"
	flagPortExtraDHCPOption  = "extra-dhcp-option"
	flagPortEnableUplink     = "enable-uplink-status-propagation"
	flagPortDisableUplink    = "disable-uplink-status-propagation"
	flagPortDataPlaneStatus  = "data-plane-status"

	// post-Zed / driver-specific attributes (upstream port.py
	// _add_updatable_args, CreatePort, UnsetPort, ListPort)
	flagPortTrusted             = "trusted"
	flagPortNotTrusted          = "not-trusted"
	flagPortHint                = "hint"
	flagPortHints               = "hints"
	flagPortNUMARequired        = "numa-policy-required"
	flagPortNUMAPreferred       = "numa-policy-preferred"
	flagPortNUMASocket          = "numa-policy-socket"
	flagPortNUMALegacy          = "numa-policy-legacy"
	flagPortNUMAPolicy          = "numa-policy"
	flagPortDeviceProfile       = "device-profile"
	flagPortHardwareOffloadType = "hardware-offload-type"
	flagPortPVLANType           = "pvlan-type"
	flagPortPVLANCommunity      = "pvlan-community"
	flagPortPVLAN               = "pvlan"
	flagPortNoPVLAN             = "no-pvlan"
)

// portPVLANTypes are upstream's --pvlan-type choices (neutron-lib
// services/pvlan/constants.py PVLAN_TYPES).
var portPVLANTypes = []string{"promiscuous", "isolated", "community"}

// portVNICTypes are the --vnic-type values upstream accepts (port.py
// _add_updatable_args choices); every one predates Zed.
var portVNICTypes = []string{
	"accelerator-direct", "accelerator-direct-physical", "direct", "direct-physical",
	"macvtap", "normal", "baremetal", "virtio-forwarder", "smart-nic", "vdpa", "remote-managed",
}

func newPortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Manage ports",
	}
	cmd.AddCommand(newPortListCommand(a, o))
	cmd.AddCommand(newPortShowCommand(a, o))
	cmd.AddCommand(newPortCreateCommand(a, o))
	cmd.AddCommand(newPortDeleteCommand(a, o))
	cmd.AddCommand(newPortSetCommand(a, o))
	cmd.AddCommand(newPortUnsetCommand(a, o))
	return cmd
}

// getPort reads a port with the extension attributes, replacing
// ports.Get(...).Extract(), which would drop them.
func getPort(ctx context.Context, client *gophercloud.ServiceClient, id string) (*portExt, error) {
	var p portExt
	if err := ports.Get(ctx, client, id).ExtractInto(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// formatFixedIPs renders a port's fixed IPs the way upstream OSC does — one
// "key='value'" entry per line — rather than as the raw JSON blob the generic
// cell renderer would produce for a []ports.IP.
func formatFixedIPs(ips []ports.IP) string {
	lines := make([]string, 0, len(ips))
	for _, ip := range ips {
		lines = append(lines, fmt.Sprintf("ip_address='%s', subnet_id='%s'", ip.IPAddress, ip.SubnetID))
	}
	return strings.Join(lines, "\n")
}

// formatAddressPairs renders allowed_address_pairs in the same shape. The
// mac_address key is omitted when neutron did not set one, matching the API.
func formatAddressPairs(pairs []ports.AddressPair) string {
	lines := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if p.MACAddress == "" {
			lines = append(lines, fmt.Sprintf("ip_address='%s'", p.IPAddress))
			continue
		}
		lines = append(lines, fmt.Sprintf("ip_address='%s', mac_address='%s'", p.IPAddress, p.MACAddress))
	}
	return strings.Join(lines, "\n")
}

func portShowFields(p *portExt) ([]string, []any) {
	fields := []string{
		"id", "name", "network_id", "mac_address", "status", "admin_state_up",
		"device_owner", "device_id", "fixed_ips", "security_groups",
		"allowed_address_pairs", "port_security_enabled",
		"binding_host_id", "binding_profile", "binding_vif_details", "binding_vif_type", "binding_vnic_type",
		"data_plane_status", "dns_assignment", "dns_domain", "dns_name", "extra_dhcp_opts",
		"propagate_uplink_status", "qos_network_policy_id", "qos_policy_id",
		"device_profile", "hardware_offload_type", "hints", "ip_allocation", "numa_affinity_policy",
		"pvlan_type", "pvlan_community", "resource_request", "trusted", "revision_number",
		"description", "project_id", "tags", "created_at", "updated_at",
	}
	values := []any{
		p.ID, p.Name, p.NetworkID, p.MACAddress, p.Status, p.AdminStateUp,
		p.DeviceOwner, p.DeviceID, formatFixedIPs(p.FixedIPs), p.SecurityGroups,
		formatAddressPairs(p.AllowedAddressPairs), optionalBool(p.PortSecurityEnabled),
		p.BindingHostID, mapOrNil(p.BindingProfile), mapOrNil(p.BindingVIFDetails), p.BindingVIFType, p.BindingVNICType,
		p.DataPlaneStatus, listOrNil(p.DNSAssignment), p.DNSDomain, p.DNSName, listOrNil(p.ExtraDHCPOpts),
		p.PropagateUplinkStatus, p.QoSNetworkPolicyID, p.QoSPolicyID,
		p.DeviceProfile, p.HardwareOffloadType, mapOrNil(p.Hints), p.IPAllocation, p.NUMAAffinityPolicy,
		optionalString(p.PVLANType), optionalString(p.PVLANCommunity), mapOrNil(p.ResourceRequest),
		optionalBool(p.Trusted), p.RevisionNumber,
		p.Description, p.ProjectID, p.Tags, p.CreatedAt, p.UpdatedAt,
	}
	return fields, values
}

// optionalString renders an extension string that may be null: empty when
// neutron sent null (or the extension is absent) rather than a quoted "".
func optionalString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// optionalBool renders an extension boolean: nil (empty) when the deployment
// does not run the extension, rather than a misleading "false".
func optionalBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// mapOrNil and listOrNil render an absent or empty structured attribute as an
// empty cell instead of the literal "null"/"{}" the JSON fallback would print.
func mapOrNil(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func listOrNil(l []map[string]any) any {
	if len(l) == 0 {
		return nil
	}
	return l
}

type portListFlags struct {
	name          string
	network       string
	router        string
	server        string
	deviceID      string
	deviceOwner   string
	host          string
	macAddress    string
	status        string
	project       string
	projectDomain string
	securityGroup []string
	fixedIP       []string
	long          bool
	allProjects   bool
	// --pvlan-type/--pvlan-community are server-side filters; --pvlan/--no-pvlan
	// filter the result client-side on pvlan_type, as upstream does.
	pvlanType      string
	pvlanCommunity string
	pvlan          bool
	noPVLAN        bool
	tagFilterFlags
}

// pvlanColumns reports whether any PVLAN filter was given, which is when
// upstream adds the PVLAN Type and PVLAN Community columns.
func (f *portListFlags) pvlanColumns() bool {
	return f.pvlan || f.noPVLAN || f.pvlanType != "" || f.pvlanCommunity != ""
}

// keepPVLAN applies --pvlan/--no-pvlan: a port counts as PVLAN-enabled when
// neutron reports a pvlan_type for it.
func (f *portListFlags) keepPVLAN(list []portExt) []portExt {
	if !f.pvlan && !f.noPVLAN {
		return list
	}
	return slices.DeleteFunc(list, func(p portExt) bool { return (p.PVLANType != nil) != f.pvlan })
}

// portListDeps supplies the secondary service clients `port list` may need:
// compute to resolve --server and identity to resolve --project. Both are
// derived lazily so listing ports never contacts a service the invocation does
// not filter on; tests pass closures over a mock endpoint.
type portListDeps struct {
	compute  func() (*gophercloud.ServiceClient, error)
	identity func() (*gophercloud.ServiceClient, error)
}

func newPortListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ports",
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
			deps := portListDeps{compute: session.Compute, identity: session.Identity}
			return runPortList(ctx, client, o, f, deps, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "list only ports with this name")
	fl.StringVar(&f.network, "network", "", "list only ports on this network (name or ID)")
	fl.StringVar(&f.router, "router", "", "list only ports attached to this router (name or ID)")
	fl.StringVar(&f.server, "server", "", "list only ports attached to this server (name or ID)")
	fl.StringVar(&f.deviceID, "device-id", "", "list only ports with this device ID")
	fl.StringVar(&f.deviceOwner, flagDeviceOwner, "", "list only ports with this device owner")
	fl.StringVar(&f.host, flagPortHost, "", "list only ports bound to this host ID")
	fl.StringVar(&f.macAddress, flagMACAddress, "", "list only ports with this MAC address")
	fl.StringVar(&f.status, "status", "", "list only ports with this status (ACTIVE, BUILD, DOWN, ERROR)")
	fl.StringVar(&f.project, flagProject, "", "list only ports in this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", "domain owning --project (name or ID)")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "list only ports in this security group (name or ID, repeatable)")
	// OSC form: --fixed-ip subnet=<subnet>,ip-address=<ip>,ip-substring=<substr>; repeatable.
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil, "filter by fixed IP: subnet=/ip-address=/ip-substring= pairs; repeatable")
	fl.StringVar(&f.pvlanType, flagPortPVLANType, "", "list only ports with this PVLAN type (requires the pvlan extension)")
	fl.StringVar(&f.pvlanCommunity, flagPortPVLANCommunity, "",
		"list only ports in this PVLAN community (requires the pvlan extension)")
	fl.BoolVar(&f.pvlan, flagPortPVLAN, false, "list only ports with PVLAN enabled (requires the pvlan extension)")
	fl.BoolVar(&f.noPVLAN, flagPortNoPVLAN, false, "list only ports with PVLAN disabled (requires the pvlan extension)")
	bindTagFilterFlags(fl, &f.tagFilterFlags, "ports")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	allprojects.Bind(cmd, &f.allProjects, allProjectsPortList)
	// Upstream OSC models these three as one device filter; they all set device_id.
	cmd.MarkFlagsMutuallyExclusive("router", "server", "device-id")
	cmd.MarkFlagsMutuallyExclusive(flagProject, "all-projects")
	cmd.MarkFlagsMutuallyExclusive(flagPortPVLAN, flagPortNoPVLAN)
	return cmd
}

// allProjectsPortList is the --all-projects help text. The flag is koc-native:
// upstream OSC gives `port list` no cross-project option because neutron needs
// none — it scopes a list to the caller's project only when the token is not an
// admin one, so an admin already sees every project's ports and there is no
// `all_tenants` query parameter to send (neutron's filter-validation extension
// would reject one outright). What the listing lacked was any way to tell whose
// port a row is, so --all-projects is presentational here: it adds the Project
// ID column, and an `openstack`-shaped invocation carrying the flag stops
// failing on an unknown flag.
const allProjectsPortList = "list ports across all projects (admin); an admin token already sees them all, " +
	"so this only adds the Project ID column"

// portListOpts adds the query parameters neutron accepts but gophercloud's
// ports.ListOpts does not model — binding:host_id behind --host, and the pvlan
// extension's pvlan_type/pvlan_community.
type portListOpts struct {
	ports.ListOpts
	hostID         string
	pvlanType      string
	pvlanCommunity string
}

func (opts portListOpts) ToPortListQuery() (string, error) {
	q, err := opts.ListOpts.ToPortListQuery()
	extra := url.Values{}
	for key, v := range map[string]string{
		"binding:host_id": opts.hostID, "pvlan_type": opts.pvlanType, "pvlan_community": opts.pvlanCommunity,
	} {
		if v != "" {
			extra.Set(key, v)
		}
	}
	return withQueryValues(q, err, extra)
}

// portExt is a Port decorated with the extension attributes gophercloud does
// not model: trunk_details (`port list --long` renders its sub_ports as "Trunk
// subports"), port_security_enabled, and the binding/QoS/DNS/DHCP attributes
// `port show` displays. Every part is an anonymous embed so ExtractPortsInto
// populates it — that extraction path decodes each embedded struct on its own,
// so an attribute has to arrive via a flat extension struct (as with MTUExt on
// networkExt) rather than a named pointer field.
type portExt struct {
	ports.Port
	TrunkDetailsExt
	PortSecurityExt
	PortAttrsExt
}

// PortSecurityExt carries the port_security_enabled attribute, which
// gophercloud's ports.Port does not model. `port set
// --enable/--disable-port-security` writes it, and without this read side the
// flag's effect would be unverifiable from koc. Exported and flat for the same
// reason as TrunkDetailsExt.
type PortSecurityExt struct {
	PortSecurityEnabled *bool `json:"port_security_enabled"`
}

// PortAttrsExt carries the portbindings, QoS, DNS-integration, extra-DHCP-opt
// and data-plane-status attributes (propagate_uplink_status is one ports.Port
// already models).
type PortAttrsExt struct {
	BindingHostID      string           `json:"binding:host_id"`
	BindingProfile     map[string]any   `json:"binding:profile"`
	BindingVIFDetails  map[string]any   `json:"binding:vif_details"`
	BindingVIFType     string           `json:"binding:vif_type"`
	BindingVNICType    string           `json:"binding:vnic_type"`
	DataPlaneStatus    string           `json:"data_plane_status"`
	DNSAssignment      []map[string]any `json:"dns_assignment"`
	DNSDomain          string           `json:"dns_domain"`
	DNSName            string           `json:"dns_name"`
	ExtraDHCPOpts      []map[string]any `json:"extra_dhcp_opts"`
	QoSNetworkPolicyID string           `json:"qos_network_policy_id"`
	QoSPolicyID        string           `json:"qos_policy_id"`
	// post-Zed and driver-specific extensions; each is absent (zero) on a
	// cloud that does not run its extension.
	DeviceProfile       string         `json:"device_profile"`
	HardwareOffloadType string         `json:"hardware_offload_type"`
	Hints               map[string]any `json:"hints"`
	IPAllocation        string         `json:"ip_allocation"`
	NUMAAffinityPolicy  string         `json:"numa_affinity_policy"`
	PVLANType           *string        `json:"pvlan_type"`
	PVLANCommunity      *string        `json:"pvlan_community"`
	ResourceRequest     map[string]any `json:"resource_request"`
	Trusted             *bool          `json:"trusted"`
}

// TrunkDetailsExt carries the trunk_details attribute. It stays exported
// because gophercloud's extraction reflects over the embedded field and cannot
// address an unexported one.
type TrunkDetailsExt struct {
	TrunkDetails trunkDetails `json:"trunk_details"`
}

type trunkDetails struct {
	TrunkID  string         `json:"trunk_id"`
	SubPorts []trunkSubPort `json:"sub_ports"`
}

type trunkSubPort struct {
	PortID           string `json:"port_id"`
	SegmentationID   int    `json:"segmentation_id"`
	SegmentationType string `json:"segmentation_type"`
	MACAddress       string `json:"mac_address"`
}

// resolvePortDeviceFilters settles the filters that select by attached device.
// --router and --server both narrow neutron's device_id, so they share a field
// and are resolved together.
func resolvePortDeviceFilters(ctx context.Context, client *gophercloud.ServiceClient,
	f *portListFlags, deps portListDeps, opts *portListOpts,
) error {
	if f.router != "" {
		routerID, err := resolveRouterID(ctx, client, f.router)
		if err != nil {
			return err
		}
		opts.DeviceID = routerID
	}
	switch {
	case resolve.IsUUID(f.server):
		// Already an ID — no nova round-trip, so `--server <uuid>` works even
		// where the compute service is unreachable.
		opts.DeviceID = f.server
	case f.server != "":
		compute, err := secondaryClient(deps.compute, "compute", "--server")
		if err != nil {
			return err
		}
		serverID, err := resolve.ServerID(ctx, compute, f.server)
		if err != nil {
			return err
		}
		opts.DeviceID = serverID
	}
	return nil
}

// resolvePortOwnerFilters settles the filters that select by owning network or
// project, each reaching into its own service only when the value is a name.
func resolvePortOwnerFilters(ctx context.Context, client *gophercloud.ServiceClient,
	f *portListFlags, deps portListDeps, opts *portListOpts,
) error {
	if f.network != "" {
		networkID, err := resolveNetworkID(ctx, client, f.network)
		if err != nil {
			return err
		}
		opts.NetworkID = networkID
	}
	switch {
	case resolve.IsUUID(f.project):
		opts.ProjectID = f.project
	case f.project != "":
		identity, err := secondaryClient(deps.identity, "identity", "--project")
		if err != nil {
			return err
		}
		projectID, err := resolve.ProjectIDInDomain(ctx, identity, f.project, f.projectDomain)
		if err != nil {
			return err
		}
		opts.ProjectID = projectID
	}
	return nil
}

// resolvePortAddressFilters settles the repeatable filters that name security
// groups and fixed IPs.
func resolvePortAddressFilters(ctx context.Context, client *gophercloud.ServiceClient,
	f *portListFlags, opts *portListOpts,
) error {
	if len(f.securityGroup) > 0 {
		sgIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return err
		}
		opts.SecurityGroups = sgIDs
	}
	for _, spec := range f.fixedIP {
		fip, err := parseFixedIPFilter(ctx, client, spec)
		if err != nil {
			return err
		}
		opts.FixedIPs = append(opts.FixedIPs, fip)
	}
	return nil
}

func runPortList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portListFlags, deps portListDeps, w io.Writer) error {
	status, err := normalizePortStatus(f.status)
	if err != nil {
		return err
	}
	opts := portListOpts{
		ListOpts: ports.ListOpts{
			Name:        f.name,
			DeviceID:    f.deviceID,
			DeviceOwner: f.deviceOwner,
			MACAddress:  f.macAddress,
			Status:      status,
		},
		hostID:         f.host,
		pvlanType:      f.pvlanType,
		pvlanCommunity: f.pvlanCommunity,
	}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
	if err := resolvePortDeviceFilters(ctx, client, f, deps, &opts); err != nil {
		return err
	}
	if err := resolvePortOwnerFilters(ctx, client, f, deps, &opts); err != nil {
		return err
	}
	if err := resolvePortAddressFilters(ctx, client, f, &opts); err != nil {
		return err
	}
	pages, err := ports.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing ports: %w", err)
	}
	var all []portExt
	if err := ports.ExtractPortsInto(pages, &all); err != nil {
		return fmt.Errorf("parsing port list: %w", err)
	}
	// A listing narrowed to one project is single-project whatever the flag says,
	// so it keeps the upstream columns — this matters because --all-projects also
	// defaults from ALL_PROJECTS in the environment.
	all = f.keepPVLAN(all)
	return o.WriteList(w, portListTable(all, portListColumns{
		long: f.long, allProjects: f.allProjects && f.project == "", pvlan: f.pvlanColumns(),
	}))
}

// portListColumns selects the optional column groups of `port list`.
type portListColumns struct {
	long, allProjects, pvlan bool
}

// portListTable renders the list. A cross-project listing gains a Project ID
// column, because without it the rows of a multi-project result are
// indistinguishable — the same reason designate's `zone list` inserts one. A
// PVLAN-filtered listing gains upstream's PVLAN Type and PVLAN Community.
func portListTable(list []portExt, c portListColumns) output.Table {
	long, allProjects := c.long, c.allProjects
	cols := []string{"ID", "Name", "MAC Address", "Fixed IP Addresses", "Status"}
	if c.pvlan {
		cols = append(cols, "PVLAN Type", "PVLAN Community")
	}
	if long {
		cols = append(cols, "Security Groups", "Device Owner", "Tags", "Trunk subports")
	}
	if allProjects {
		cols = slices.Insert(cols, 1, "Project ID")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for i := range list {
		p := &list[i]
		row := []any{p.ID, p.Name, p.MACAddress, formatFixedIPs(p.FixedIPs), p.Status}
		if c.pvlan {
			row = append(row, optionalString(p.PVLANType), optionalString(p.PVLANCommunity))
		}
		if long {
			row = append(row, p.SecurityGroups, p.DeviceOwner, p.Tags, p.TrunkDetails.SubPorts)
		}
		if allProjects {
			row = slices.Insert(row, 1, any(p.ProjectID))
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// normalizePortStatus upper-cases --status and rejects anything neutron does not
// report, since the API matches the value case-sensitively.
func normalizePortStatus(status string) (string, error) {
	if status == "" {
		return "", nil
	}
	up := strings.ToUpper(status)
	switch up {
	case "ACTIVE", "BUILD", "DOWN", "ERROR":
		return up, nil
	default:
		return "", fmt.Errorf("invalid --status %q: want one of ACTIVE, BUILD, DOWN, ERROR", status)
	}
}

// secondaryClient derives one of the optional cross-service clients, naming the
// flag that needed it when the caller supplied no way to reach that service.
func secondaryClient(derive func() (*gophercloud.ServiceClient, error), service, flag string) (*gophercloud.ServiceClient, error) {
	if derive == nil {
		return nil, fmt.Errorf("%s requires the %s service", flag, service)
	}
	return derive()
}

func newPortShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <port>",
		Short: "Show details of a port",
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
			return runPortShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runPortShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolvePortID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	p, err := getPort(ctx, client, id)
	if err != nil {
		return fmt.Errorf("getting port %s: %w", nameOrID, err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

// portAttrFlags are the flags create and set share and turn into body
// attributes the same way (upstream port.py _get_attrs). Most are extension
// attributes ports.CreateOpts/UpdateOpts lack, so they travel through the
// bodyExt adapters; uplink status propagation is the one gophercloud models.
type portAttrFlags struct {
	vnicType        string
	qosPolicy       string
	dnsName         string
	dnsDomain       string
	extraDHCPOption []string
	enableUplink    bool
	disableUplink   bool
	bindingProfile  []string
	extraProperty   []string
	portPostZedFlags
}

// portPostZedFlags are the post-Zed / driver-specific attributes create and
// set share (upstream _add_updatable_args): each needs a neutron extension a
// Zed cloud may not run, which the help text names.
type portPostZedFlags struct {
	trusted        bool
	notTrusted     bool
	numaRequired   bool
	numaPreferred  bool
	numaSocket     bool
	numaLegacy     bool
	hint           []string
	pvlanType      string
	pvlanCommunity string
}

func bindPortPostZedFlags(cmd *cobra.Command, f *portPostZedFlags) {
	fl := cmd.Flags()
	fl.BoolVar(&f.trusted, flagPortTrusted, false,
		"mark the port trusted; neutron passes this on in binding:profile (requires the port-trusted-vif extension)")
	fl.BoolVar(&f.notTrusted, flagPortNotTrusted, false,
		"mark the port not trusted (requires the port-trusted-vif extension)")
	fl.BoolVar(&f.numaRequired, flagPortNUMARequired, false,
		"schedule the port with NUMA affinity policy required (requires the port-numa-affinity-policy extension)")
	fl.BoolVar(&f.numaPreferred, flagPortNUMAPreferred, false,
		"schedule the port with NUMA affinity policy preferred (requires the port-numa-affinity-policy extension)")
	fl.BoolVar(&f.numaSocket, flagPortNUMASocket, false,
		"schedule the port with NUMA affinity policy socket (requires the port-numa-affinity-policy-socket extension)")
	fl.BoolVar(&f.numaLegacy, flagPortNUMALegacy, false,
		"schedule the port with NUMA affinity policy legacy (requires the port-numa-affinity-policy extension)")
	fl.StringArrayVar(&f.hint, flagPortHint, nil,
		"port hint as ovs-tx-steering=thread|hash (needs port-hint-ovs-tx-steering) or as neutron's hints JSON; "+
			"repeatable (requires the port-hints extension)")
	fl.StringVar(&f.pvlanType, flagPortPVLANType, "",
		"private VLAN type of the port ("+strings.Join(portPVLANTypes, ", ")+") (requires the pvlan extension)")
	fl.StringVar(&f.pvlanCommunity, flagPortPVLANCommunity, "",
		"private VLAN community of the port; required with --pvlan-type community (requires the pvlan extension)")
	cmd.MarkFlagsMutuallyExclusive(flagPortTrusted, flagPortNotTrusted)
	cmd.MarkFlagsMutuallyExclusive(flagPortNUMARequired, flagPortNUMAPreferred, flagPortNUMASocket, flagPortNUMALegacy)
}

// attrs adds the post-Zed attributes to attrs, in upstream _get_attrs /
// take_action terms: --pvlan-community is sent whenever given (so an empty
// value goes out), the others only when set. --hint is validated, its alias
// expanded, and its extensions checked before anything is sent, as upstream
// does.
func (f *portPostZedFlags) attrs(ctx context.Context, client *gophercloud.ServiceClient, flags flagSet, attrs map[string]any) error {
	if policy := f.numaPolicy(); policy != "" {
		attrs["numa_affinity_policy"] = policy
	}
	switch {
	case f.trusted:
		attrs["trusted"] = true
	case f.notTrusted:
		attrs["trusted"] = false
	}
	if flags.Changed(flagPortPVLANType) {
		if !slices.Contains(portPVLANTypes, f.pvlanType) {
			return fmt.Errorf("invalid --%s %q: want one of %s", flagPortPVLANType, f.pvlanType, strings.Join(portPVLANTypes, ", "))
		}
		attrs["pvlan_type"] = f.pvlanType
	}
	if flags.Changed(flagPortPVLANCommunity) {
		attrs["pvlan_community"] = f.pvlanCommunity
	}
	if len(f.hint) == 0 {
		return nil
	}
	hints, err := parsePortHints(f.hint)
	if err != nil || hints == nil {
		return err
	}
	if err := requirePortHintExtensions(ctx, client); err != nil {
		return err
	}
	attrs["hints"] = hints
	return nil
}

// numaPolicy is upstream's if/elif chain over the (mutually exclusive) NUMA
// flags.
func (f *portPostZedFlags) numaPolicy() string {
	switch {
	case f.numaRequired:
		return "required"
	case f.numaPreferred:
		return "preferred"
	case f.numaSocket:
		return "socket"
	case f.numaLegacy:
		return "legacy"
	default:
		return ""
	}
}

// parsePortHints merges the --hint values the way upstream's JSONKeyValueAction
// does — a JSON object is merged in, anything else is split once at '=' — then
// applies upstream's _validate_port_hints and _expand_port_hint_aliases: the
// merged hints must be exactly one ovs-tx-steering alias or its fully
// specified JSON form, and the alias is expanded into the nested hints
// attribute. An empty result (a bare '{}') sends nothing, as upstream's
// "if parsed_args.hint" skips it.
func parsePortHints(specs []string) (map[string]any, error) {
	merged := map[string]any{}
	for _, spec := range specs {
		if obj, ok := decodeJSONObject(spec); ok {
			maps.Copy(merged, obj)
			continue
		}
		k, v, found := strings.Cut(spec, "=")
		if !found {
			return nil, fmt.Errorf("--%s %q: expected <alias>=<value> or a JSON object", flagPortHint, spec)
		}
		merged[k] = v
	}
	if len(merged) == 0 {
		return nil, nil
	}
	steering, ok := portHintTxSteering(merged)
	if !ok {
		return nil, fmt.Errorf("invalid --%s: want ovs-tx-steering=thread, ovs-tx-steering=hash, "+
			`or {"openvswitch":{"other_config":{"tx-steering":"thread|hash"}}}`, flagPortHint)
	}
	return map[string]any{"openvswitch": map[string]any{"other_config": map[string]any{"tx-steering": steering}}}, nil
}

// portHintTxSteering recognises the four hint forms upstream accepts and
// returns their tx-steering value.
func portHintTxSteering(hints map[string]any) (string, bool) {
	valid := func(v any) (string, bool) {
		s, ok := v.(string)
		return s, ok && (s == "thread" || s == "hash")
	}
	only := func(m map[string]any, key string) (any, bool) {
		v, ok := m[key]
		return v, ok && len(m) == 1
	}
	if v, ok := only(hints, "ovs-tx-steering"); ok {
		return valid(v)
	}
	ovs, ok := only(hints, "openvswitch")
	if !ok {
		return "", false
	}
	ovsMap, ok := ovs.(map[string]any)
	if !ok {
		return "", false
	}
	other, ok := only(ovsMap, "other_config")
	if !ok {
		return "", false
	}
	otherMap, ok := other.(map[string]any)
	if !ok {
		return "", false
	}
	steering, ok := only(otherMap, "tx-steering")
	if !ok {
		return "", false
	}
	return valid(steering)
}

// requirePortHintExtensions is upstream's find_extension pre-check: hints need
// port-hints, and a tx-steering hint — the only kind parsePortHints lets
// through — also port-hint-ovs-tx-steering. A cloud without either refuses the
// write before it is sent.
func requirePortHintExtensions(ctx context.Context, client *gophercloud.ServiceClient) error {
	for _, alias := range []string{"port-hints", "port-hint-ovs-tx-steering"} {
		if _, err := getNetworkExtension(ctx, client, alias); err != nil {
			return fmt.Errorf("--%s is not supported by this cloud's neutron (extension %s): %w", flagPortHint, alias, err)
		}
	}
	return nil
}

// validatePVLANPort is upstream's _validate_pvlan_port, run on the flag
// attributes before --extra-property is merged: PVLAN attributes cannot go with
// disabled port security, and a community port needs its community.
func validatePVLANPort(attrs map[string]any) error {
	if hasPVLANAttrs(attrs) && attrs["port_security_enabled"] == false {
		return fmt.Errorf("PVLAN attributes cannot be set when port security is disabled")
	}
	if attrs["pvlan_type"] == "community" && !truthy(attrs["pvlan_community"]) {
		return fmt.Errorf("--%s is required when --%s is 'community'", flagPortPVLANCommunity, flagPortPVLANType)
	}
	return nil
}

// hasPVLANAttrs mirrors upstream's `attrs.get('pvlan_type') or
// attrs.get('pvlan_community')`.
func hasPVLANAttrs(attrs map[string]any) bool {
	return truthy(attrs["pvlan_type"]) || truthy(attrs["pvlan_community"])
}

// truthy is Python truthiness for the values an attrs map can hold.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	default:
		return true
	}
}

// requirePVLANNetwork is upstream's _validate_pvlan_network_port: a port can
// carry PVLAN attributes only on a network with pvlan enabled. It reads the
// network only when the final attributes (after --extra-property) carry one.
func requirePVLANNetwork(ctx context.Context, client *gophercloud.ServiceClient, attrs map[string]any, networkID string) error {
	if !hasPVLANAttrs(attrs) {
		return nil
	}
	var n struct {
		PVLAN bool `json:"pvlan"`
	}
	if err := networks.Get(ctx, client, networkID).ExtractInto(&n); err != nil {
		return fmt.Errorf("reading network %s for the PVLAN check: %w", networkID, err)
	}
	if !n.PVLAN {
		return fmt.Errorf("PVLAN attributes cannot be set on a port whose network does not have PVLAN enabled")
	}
	return nil
}

// portExplainAttrs is what explainMissingExtension is given for a port write:
// the attributes the body adapter merges in, plus the extension attributes
// gophercloud's typed opts carry (named in typed), plus the synthetic keys
// attrExtensions uses where the extension depends on the value or on the
// resource rather than on the attribute name alone.
func portExplainAttrs(attrs map[string]any, typed ...string) map[string]any {
	out := maps.Clone(attrs)
	if out == nil {
		out = map[string]any{}
	}
	for _, name := range typed {
		out[name] = true
	}
	if out["numa_affinity_policy"] == "socket" {
		out["numa_affinity_policy=socket"] = true
	}
	// dns_domain on a port is the dns-domain-ports extension, not the
	// dns-integration one the attribute means on a network.
	if v, ok := out["dns_domain"]; ok {
		delete(out, "dns_domain")
		out["port.dns_domain"] = v
	}
	return out
}

func bindPortAttrFlags(cmd *cobra.Command, f *portAttrFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.vnicType, flagPortVNICType, "", "VNIC type for the port ("+strings.Join(portVNICTypes, ", ")+")")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy to attach to the port (name or ID)")
	fl.StringVar(&f.dnsName, flagDNSName, "", "DNS name for the port (requires the dns-integration extension)")
	fl.StringVar(&f.dnsDomain, flagDNSDomain, "", "DNS domain for the port (requires the dns-domain-ports extension)")
	fl.StringArrayVar(&f.extraDHCPOption, flagPortExtraDHCPOption, nil,
		"extra DHCP option as name=<name>[,value=<value>,ip-version={4,6}] (repeatable)")
	fl.BoolVar(&f.enableUplink, flagPortEnableUplink, false, "enable uplink status propagation")
	fl.BoolVar(&f.disableUplink, flagPortDisableUplink, false, "disable uplink status propagation")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	cmd.MarkFlagsMutuallyExclusive(flagPortEnableUplink, flagPortDisableUplink)
	bindPortPostZedFlags(cmd, &f.portPostZedFlags)
}

// attrs returns the shared extension attributes, never nil so a caller can add
// its own. --dns-name/--dns-domain are sent whenever given, so an empty value
// clears them, as upstream's "is not None" does.
func (f *portAttrFlags) attrs(ctx context.Context, client *gophercloud.ServiceClient, flags flagSet) (map[string]any, error) {
	attrs := map[string]any{}
	if f.vnicType != "" {
		if !slices.Contains(portVNICTypes, f.vnicType) {
			return nil, fmt.Errorf("invalid --%s %q: want one of %s", flagPortVNICType, f.vnicType, strings.Join(portVNICTypes, ", "))
		}
		attrs["binding:vnic_type"] = f.vnicType
	}
	if f.qosPolicy != "" {
		qosID, err := resolveQoSPolicyID(ctx, client, f.qosPolicy)
		if err != nil {
			return nil, err
		}
		attrs["qos_policy_id"] = qosID
	}
	if flags.Changed(flagDNSName) {
		attrs["dns_name"] = f.dnsName
	}
	if flags.Changed(flagDNSDomain) {
		attrs["dns_domain"] = f.dnsDomain
	}
	if len(f.extraDHCPOption) > 0 {
		opts, err := parseExtraDHCPOptions(f.extraDHCPOption)
		if err != nil {
			return nil, err
		}
		attrs["extra_dhcp_opts"] = opts
	}
	if err := f.portPostZedFlags.attrs(ctx, client, flags, attrs); err != nil {
		return nil, err
	}
	return attrs, nil
}

func (f *portAttrFlags) uplink(flags flagSet) *bool {
	return enableDisable(flags, f.enableUplink, f.disableUplink, flagPortEnableUplink, flagPortDisableUplink)
}

type portCreateFlags struct {
	network             string
	fixedIP             []string
	noFixedIP           bool
	macAddress          string
	deviceOwner         string
	device              string
	host                string
	description         string
	securityGroup       []string
	noSecurityGroup     bool
	allowedAddress      []string
	enablePortSecurity  bool
	disablePortSecurity bool
	enable              bool
	disable             bool
	project             string
	projectDomain       string
	// create-only post-Zed attributes (neutron allows neither on PUT)
	deviceProfile       string
	hardwareOffloadType string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
	portAttrFlags
	tagWriteFlags
}

func newPortCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := mutuallyExclusive(cmd.Flags(), "enable", "disable"); err != nil {
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
			return runPortCreate(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.network, "network", "", "network for the port (name or ID, required)")
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil, "desired IP as subnet=<name|id>,ip-address=<ip> (repeatable)")
	fl.BoolVar(&f.noFixedIP, flagPortNoFixedIP, false, "create the port with no fixed IP")
	fl.StringVar(&f.macAddress, flagMACAddress, "", "MAC address for the port")
	fl.StringVar(&f.deviceOwner, flagDeviceOwner, "", "device owner for the port")
	fl.StringVar(&f.device, flagPortDevice, "", "device ID for the port")
	fl.StringVar(&f.host, flagPortHost, "", "bind the port to this host ID")
	fl.StringVar(&f.description, flagDescription, "", "description for the port")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "security group to associate (name or ID, repeatable)")
	fl.BoolVar(&f.noSecurityGroup, flagNoSecurityGroup, false, "create the port with no security groups")
	fl.StringArrayVar(&f.allowedAddress, flagAllowedAddress, nil, "allowed address pair as ip-address=<ip>[,mac-address=<mac>] (repeatable)")
	fl.BoolVar(&f.enablePortSecurity, flagEnablePortSecurity, false, "enable port security (security groups and anti-spoofing)")
	fl.BoolVar(&f.disablePortSecurity, flagDisablePortSecurity, false, "disable port security")
	fl.BoolVar(&f.enable, "enable", false, "create the port administratively up (default)")
	fl.BoolVar(&f.disable, "disable", false, "create the port administratively down")
	fl.StringArrayVar(&f.bindingProfile, flagPortBindingProfile, nil,
		"binding:profile data as <key>=<value> or a JSON object (repeatable)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.StringVar(&f.deviceProfile, flagPortDeviceProfile, "",
		"device profile for the port (requires the port-device-profile extension)")
	fl.StringVar(&f.hardwareOffloadType, flagPortHardwareOffloadType, "",
		"hardware offload type the port requests from the network backend, e.g. switchdev "+
			"(requires the port-hardware-offload-type extension)")
	bindPortAttrFlags(cmd, &f.portAttrFlags)
	bindTagCreateFlags(cmd, &f.tagWriteFlags, "port")
	cmd.MarkFlagsMutuallyExclusive(flagFixedIP, flagPortNoFixedIP)
	cmd.MarkFlagsMutuallyExclusive(flagSecurityGroup, flagNoSecurityGroup)
	cmd.MarkFlagsMutuallyExclusive(flagEnablePortSecurity, flagDisablePortSecurity)
	_ = cmd.MarkFlagRequired("network")
	return cmd
}

func runPortCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *portCreateFlags, flags flagSet, w io.Writer) error {
	opts, err := portCreateOpts(ctx, client, name, f, flags)
	if err != nil {
		return err
	}
	attrs, err := portCreateAttrs(ctx, client, f, flags)
	if err != nil {
		return err
	}
	if err := requirePVLANNetwork(ctx, client, attrs, opts.NetworkID); err != nil {
		return err
	}
	var p portExt
	if err := ports.Create(ctx, client, withPortCreateAttrs(opts, attrs)).ExtractInto(&p); err != nil {
		return explainMissingExtension(ctx, client, fmt.Errorf("creating port: %w", err),
			portExplainAttrs(attrs, portCreateTypedExtAttrs(opts)...))
	}
	// Tags are a sub-resource a create cannot carry on every deployment, so
	// they are set afterwards, as upstream does without the
	// tag-ports-during-bulk-creation extension.
	if p.Tags, err = applyTagsForSet(ctx, client, tagResourcePorts, p.ID, p.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := portShowFields(&p)
	return o.WriteSingle(w, fields, values)
}

// portCreateOpts builds the attributes gophercloud's ports.CreateOpts models.
func portCreateOpts(ctx context.Context, client *gophercloud.ServiceClient, name string, f *portCreateFlags, flags flagSet) (ports.CreateOpts, error) {
	networkID, err := resolveNetworkID(ctx, client, f.network)
	if err != nil {
		return ports.CreateOpts{}, err
	}
	opts := ports.CreateOpts{
		NetworkID:             networkID,
		Name:                  name,
		MACAddress:            f.macAddress,
		DeviceOwner:           f.deviceOwner,
		DeviceID:              f.device,
		Description:           f.description,
		ProjectID:             f.projectID,
		PropagateUplinkStatus: f.uplink(flags),
	}
	switch {
	case f.disable:
		opts.AdminStateUp = boolPtr(false)
	case f.enable:
		opts.AdminStateUp = boolPtr(true)
	}
	fixedIPs, err := buildFixedIPs(ctx, client, f.fixedIP)
	if err != nil {
		return opts, err
	}
	if fixedIPs != nil {
		opts.FixedIPs = fixedIPs
	}
	switch {
	case f.noSecurityGroup:
		opts.SecurityGroups = &[]string{}
	case len(f.securityGroup) > 0:
		sgIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return opts, err
		}
		opts.SecurityGroups = &sgIDs
	}
	if len(f.allowedAddress) > 0 {
		pairs, err := parseAddressPairs(f.allowedAddress)
		if err != nil {
			return opts, err
		}
		opts.AllowedAddressPairs = pairs
	}
	return opts, nil
}

// portCreateAttrs builds the attributes ports.CreateOpts lacks. --extra-property
// is merged last so it wins, as upstream's attrs.update(...) does.
func portCreateAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *portCreateFlags, flags flagSet) (map[string]any, error) {
	attrs, err := f.attrs(ctx, client, flags)
	if err != nil {
		return nil, err
	}
	if f.host != "" {
		attrs["binding:host_id"] = f.host
	}
	if len(f.bindingProfile) > 0 {
		profile, err := parseBindingProfile(f.bindingProfile)
		if err != nil {
			return nil, err
		}
		attrs["binding:profile"] = profile
	}
	// ports.CreateOpts.FixedIPs is omitempty, so an explicit empty list has to
	// be set as a raw attribute.
	if f.noFixedIP {
		attrs["fixed_ips"] = []any{}
	}
	if secure := enableDisable(flags, f.enablePortSecurity, f.disablePortSecurity,
		flagEnablePortSecurity, flagDisablePortSecurity); secure != nil {
		attrs["port_security_enabled"] = *secure
	}
	if f.deviceProfile != "" {
		attrs["device_profile"] = f.deviceProfile
	}
	if f.hardwareOffloadType != "" {
		attrs["hardware_offload_type"] = f.hardwareOffloadType
	}
	if err := validatePVLANPort(attrs); err != nil {
		return nil, err
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(attrs, extra), nil
}

// portCreateTypedExtAttrs names the extension attributes a create carries in
// gophercloud's typed opts, which the attrs map never sees.
func portCreateTypedExtAttrs(opts ports.CreateOpts) []string {
	var names []string
	if opts.PropagateUplinkStatus != nil {
		names = append(names, "propagate_uplink_status")
	}
	if opts.SecurityGroups != nil {
		names = append(names, "security_groups")
	}
	if len(opts.AllowedAddressPairs) > 0 {
		names = append(names, "allowed_address_pairs")
	}
	return names
}

// portUpdateTypedExtAttrs is the same for an update.
func portUpdateTypedExtAttrs(opts ports.UpdateOpts) []string {
	var names []string
	if opts.PropagateUplinkStatus != nil {
		names = append(names, "propagate_uplink_status")
	}
	if opts.SecurityGroups != nil {
		names = append(names, "security_groups")
	}
	if opts.AllowedAddressPairs != nil {
		names = append(names, "allowed_address_pairs")
	}
	return names
}

func buildFixedIPs(ctx context.Context, client *gophercloud.ServiceClient, specs []string) ([]ports.IP, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]ports.IP, 0, len(specs))
	for _, spec := range specs {
		ip, err := parseFixedIP(ctx, client, spec)
		if err != nil {
			return nil, err
		}
		out = append(out, ip)
	}
	return out, nil
}

func newPortDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <port> [<port> ...]",
		Short: "Delete port(s)",
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
			return runPortDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runPortDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	return batchdelete.Each(names, func(nameOrID string) error {
		id, err := resolvePortID(ctx, client, nameOrID)
		if err != nil {
			return err
		}
		if err := ports.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting port %s: %w", nameOrID, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted port %s\n", nameOrID); err != nil {
			return err
		}
		return nil
	})
}

// updatePort is the shared tail of set and unset: PUT the attributes when any
// were given (upstream skips the update when only tags change), then apply the
// tag change, then render what the port now looks like. current, when the verb
// already read the port, spares a second GET on the tags-only path.
func updatePort(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, id string,
	opts ports.UpdateOpts, attrs map[string]any, changed bool, current *portExt,
	applyTags func(context.Context, *gophercloud.ServiceClient, string, string, []string, *tagWriteFlags) ([]string, error),
	tags *tagWriteFlags, w io.Writer,
) error {
	var err error
	p := current
	switch {
	case changed:
		p = &portExt{}
		if err := ports.Update(ctx, client, id, withPortUpdateAttrs(opts, attrs)).ExtractInto(p); err != nil {
			return explainMissingExtension(ctx, client, fmt.Errorf("updating port %s: %w", ref, err),
				portExplainAttrs(attrs, portUpdateTypedExtAttrs(opts)...))
		}
	case p == nil:
		if p, err = getPort(ctx, client, id); err != nil {
			return fmt.Errorf("getting port %s: %w", ref, err)
		}
	}
	if p.Tags, err = applyTags(ctx, client, tagResourcePorts, id, p.Tags, tags); err != nil {
		return err
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

// parseAddressPairs parses the repeatable --allowed-address specs into
// ports.AddressPair values. Each spec is a comma-separated key=value list:
// ip-address=<ip>[,mac-address=<mac>], matching OSC.
func parseAddressPairs(specs []string) ([]ports.AddressPair, error) {
	pairs := make([]ports.AddressPair, 0, len(specs))
	for _, spec := range specs {
		var pair ports.AddressPair
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, v, err := splitKV(part)
			if err != nil {
				return nil, fmt.Errorf("parsing --allowed-address %q: %w", spec, err)
			}
			switch k {
			case "ip-address", "ip_address":
				pair.IPAddress = v
			case "mac-address", "mac_address":
				pair.MACAddress = v
			default:
				return nil, fmt.Errorf("parsing --allowed-address %q: unknown key %q", spec, k)
			}
		}
		if pair.IPAddress == "" {
			return nil, fmt.Errorf("--allowed-address %q requires ip-address", spec)
		}
		pairs = append(pairs, pair)
	}
	return pairs, nil
}

// parseBindingProfile is upstream's JSONKeyValueAction: each value is either a
// JSON object merged into the profile (keeping its types) or a <key>=<value>
// pair whose value is sent as a string. Later values win.
func parseBindingProfile(specs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, spec := range specs {
		if obj, ok := decodeJSONObject(spec); ok {
			maps.Copy(out, obj)
			continue
		}
		k, v, found := strings.Cut(spec, "=")
		if !found || k == "" {
			return nil, fmt.Errorf("--%s %q: expected <key>=<value> or a JSON object", flagPortBindingProfile, spec)
		}
		out[k] = v
	}
	return out, nil
}

// decodeJSONObject reports whether s is exactly one JSON object. Numbers stay
// json.Number so they reach neutron unchanged.
func decodeJSONObject(s string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil || dec.More() {
		return nil, false
	}
	return obj, true
}

// parseExtraDHCPOptions turns --extra-dhcp-option specs into neutron's
// extra_dhcp_opts entries (upstream _convert_extra_dhcp_options): name= is
// required, value= and ip-version= optional. ip-version is sent as the integer
// neutron converts it to anyway.
func parseExtraDHCPOptions(specs []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		kv, err := splitCommaKV(flagPortExtraDHCPOption, spec, "name", "value", "ip-version")
		if err != nil {
			return nil, err
		}
		name := kv["name"]
		if name == "" {
			return nil, fmt.Errorf("--%s %q requires name=", flagPortExtraDHCPOption, spec)
		}
		opt := map[string]any{"opt_name": name}
		if v, ok := kv["value"]; ok {
			opt["opt_value"] = v
		}
		if v, ok := kv["ip-version"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil || (n != 4 && n != 6) {
				return nil, fmt.Errorf("--%s %q: ip-version must be 4 or 6", flagPortExtraDHCPOption, spec)
			}
			opt["ip_version"] = n
		}
		out = append(out, opt)
	}
	return out, nil
}

// splitCommaKV is osc-lib's MultiKeyValueCommaAction: comma-separated
// key=value pairs where a piece with no '=' continues the previous value, so a
// value may itself contain commas (value=a.example.com,b.example.com).
func splitCommaKV(flag, spec string, allowed ...string) (map[string]string, error) {
	kv := map[string]string{}
	key := ""
	for _, part := range strings.Split(spec, ",") {
		k, v, found := strings.Cut(part, "=")
		switch {
		case !found && key == "":
			return nil, fmt.Errorf("parsing --%s %q: a key=value pair is required, got %q", flag, spec, part)
		case !found:
			kv[key] += "," + part
		case k == "":
			return nil, fmt.Errorf("parsing --%s %q: a key must be given before '='", flag, spec)
		case !slices.Contains(allowed, k):
			return nil, fmt.Errorf("parsing --%s %q: unknown key %q (want %s)", flag, spec, k, strings.Join(allowed, ", "))
		default:
			kv[k] = v
			key = k
		}
	}
	return kv, nil
}

type portSetFlags struct {
	name            string
	fixedIP         []string
	noFixedIP       bool
	description     string
	securityGroup   []string
	noSecurityGroup bool
	enable          bool
	disable         bool

	allowedAddress      []string
	noAllowedAddress    bool
	enablePortSecurity  bool
	disablePortSecurity bool
	host                string
	device              string
	deviceOwner         string
	macAddress          string
	noBindingProfile    bool
	dataPlaneStatus     string
	portAttrFlags
	tagWriteFlags
}

func newPortSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <port>",
		Short: "Set port properties",
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
			return runPortSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	// The list flags extend what the port already has, as upstream's do; their
	// --no-* twin clears it first, so giving both overwrites the list.
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new port name")
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil,
		"fixed IP to add as subnet=<name|id>,ip-address=<ip> (repeatable)")
	fl.BoolVar(&f.noFixedIP, flagPortNoFixedIP, false,
		"clear the port's fixed IPs; combine with --fixed-ip to overwrite them")
	fl.StringVar(&f.description, flagDescription, "", "new port description")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "security group to add (name or ID, repeatable)")
	fl.BoolVar(&f.noSecurityGroup, flagNoSecurityGroup, false,
		"clear the port's security groups; combine with --security-group to overwrite them")
	fl.BoolVar(&f.enable, "enable", false, "set the port administratively up")
	fl.BoolVar(&f.disable, "disable", false, "set the port administratively down")
	fl.StringArrayVar(&f.allowedAddress, flagAllowedAddress, nil,
		"allowed address pair to add as ip-address=<ip>[,mac-address=<mac>] (repeatable)")
	fl.BoolVar(&f.noAllowedAddress, flagNoAllowedAddress, false,
		"clear the port's allowed address pairs; combine with --allowed-address to overwrite them")
	fl.BoolVar(&f.enablePortSecurity, flagEnablePortSecurity, false, "enable port security (security groups and anti-spoofing)")
	fl.BoolVar(&f.disablePortSecurity, flagDisablePortSecurity, false, "disable port security")
	fl.StringVar(&f.host, flagPortHost, "", "binding host ID for the port")
	fl.StringVar(&f.device, flagPortDevice, "", "device ID the port is attached to")
	fl.StringVar(&f.deviceOwner, flagDeviceOwner, "", "device owner of the port")
	fl.StringVar(&f.macAddress, flagMACAddress, "", "MAC address for the port (admin)")
	fl.StringArrayVar(&f.bindingProfile, flagPortBindingProfile, nil,
		"binding:profile data to merge in, as <key>=<value> or a JSON object (repeatable)")
	fl.BoolVar(&f.noBindingProfile, flagPortNoBindingProfile, false,
		"clear the port's binding:profile; combine with --binding-profile to overwrite it")
	fl.StringVar(&f.dataPlaneStatus, flagPortDataPlaneStatus, "",
		"data plane status of the port (ACTIVE, DOWN; requires the data-plane-status extension)")
	bindPortAttrFlags(cmd, &f.portAttrFlags)
	bindTagSetFlags(fl, &f.tagWriteFlags, "port")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive(flagEnablePortSecurity, flagDisablePortSecurity)
	return cmd
}

// portSnapshot reads the port at most once, and only for the flags that
// extend its current value rather than replace it.
type portSnapshot struct {
	load func() (*portExt, error)
	port *portExt
}

func (s *portSnapshot) get() (*portExt, error) {
	if s.port == nil {
		p, err := s.load()
		if err != nil {
			return nil, fmt.Errorf("reading port before set: %w", err)
		}
		s.port = p
	}
	return s.port, nil
}

// extendOrReplace is upstream's set semantics for a list attribute: with its
// --no-* flag the list starts empty, otherwise from the port's current entries,
// and the given ones are appended.
func extendOrReplace[T any](reset bool, snap *portSnapshot, current func(*portExt) []T, add []T) ([]T, error) {
	out := []T{}
	if !reset {
		p, err := snap.get()
		if err != nil {
			return nil, err
		}
		out = append(out, current(p)...)
	}
	return append(out, add...), nil
}

func runPortSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *portSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolvePortID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts, changed := portSetScalarOpts(f, flags)
	attrs, err := portSetAttrs(ctx, client, f, flags)
	if err != nil {
		return err
	}
	snap := &portSnapshot{load: func() (*portExt, error) { return getPort(ctx, client, id) }}
	listsChanged, err := portSetLists(ctx, client, f, flags, snap, &opts, attrs)
	if err != nil {
		return err
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	changed = changed || listsChanged || len(attrs) > 0
	if !changed && !f.given() {
		return fmt.Errorf("port set requires at least one attribute flag")
	}
	if hasPVLANAttrs(attrs) {
		// upstream checks the port's own network, so the port is read for it
		p, err := snap.get()
		if err != nil {
			return err
		}
		if err := requirePVLANNetwork(ctx, client, attrs, p.NetworkID); err != nil {
			return err
		}
	}
	// A list computed from what was just read is pinned to that revision, so a
	// concurrent change is rejected rather than overwritten (as in port unset).
	if snap.port != nil {
		revision := snap.port.RevisionNumber
		opts.RevisionNumber = &revision
	}
	return updatePort(ctx, client, o, nameOrID, id, opts, attrs, changed, snap.port, applyTagsForSet, &f.tagWriteFlags, w)
}

// portSetScalarOpts builds the plain attributes ports.UpdateOpts models.
func portSetScalarOpts(f *portSetFlags, flags flagSet) (ports.UpdateOpts, bool) {
	opts := ports.UpdateOpts{}
	changed := false
	if f.name != "" {
		opts.Name = &f.name
		changed = true
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
		changed = true
	}
	if state := enableDisable(flags, f.enable, f.disable); state != nil {
		opts.AdminStateUp = state
		changed = true
	}
	if flags.Changed(flagPortDevice) {
		opts.DeviceID = &f.device
		changed = true
	}
	if flags.Changed(flagDeviceOwner) {
		opts.DeviceOwner = &f.deviceOwner
		changed = true
	}
	if flags.Changed(flagMACAddress) {
		opts.MACAddress = &f.macAddress
		changed = true
	}
	if uplink := f.uplink(flags); uplink != nil {
		opts.PropagateUplinkStatus = uplink
		changed = true
	}
	return opts, changed
}

// portSetAttrs builds the extension attributes ports.UpdateOpts lacks.
func portSetAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *portSetFlags, flags flagSet) (map[string]any, error) {
	attrs, err := f.attrs(ctx, client, flags)
	if err != nil {
		return nil, err
	}
	if flags.Changed(flagPortHost) {
		attrs["binding:host_id"] = f.host
	}
	if secure := enableDisable(flags, f.enablePortSecurity, f.disablePortSecurity,
		flagEnablePortSecurity, flagDisablePortSecurity); secure != nil {
		attrs["port_security_enabled"] = *secure
	}
	if f.dataPlaneStatus != "" {
		status := strings.ToUpper(f.dataPlaneStatus)
		if status != "ACTIVE" && status != "DOWN" {
			return nil, fmt.Errorf("invalid --%s %q: want ACTIVE or DOWN", flagPortDataPlaneStatus, f.dataPlaneStatus)
		}
		attrs["data_plane_status"] = status
	}
	if err := validatePVLANPort(attrs); err != nil {
		return nil, err
	}
	return attrs, nil
}

// portSetLists applies the four read-modify-write flags: fixed IPs, security
// groups and allowed address pairs extend their lists, --binding-profile
// merges into the profile. The port is read only if one of them needs it.
func portSetLists(ctx context.Context, client *gophercloud.ServiceClient, f *portSetFlags, flags flagSet,
	snap *portSnapshot, opts *ports.UpdateOpts, attrs map[string]any,
) (bool, error) {
	changed := false
	if f.noFixedIP || flags.Changed(flagFixedIP) {
		add, err := buildFixedIPs(ctx, client, f.fixedIP)
		if err != nil {
			return false, err
		}
		ips, err := extendOrReplace(f.noFixedIP, snap, func(p *portExt) []ports.IP { return p.FixedIPs }, add)
		if err != nil {
			return false, err
		}
		opts.FixedIPs = ips
		changed = true
	}
	if f.noSecurityGroup || flags.Changed(flagSecurityGroup) {
		add, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return false, err
		}
		sgs, err := extendOrReplace(f.noSecurityGroup, snap, func(p *portExt) []string { return p.SecurityGroups }, add)
		if err != nil {
			return false, err
		}
		opts.SecurityGroups = &sgs
		changed = true
	}
	if f.noAllowedAddress || flags.Changed(flagAllowedAddress) {
		add, err := parseAddressPairs(f.allowedAddress)
		if err != nil {
			return false, err
		}
		pairs, err := extendOrReplace(f.noAllowedAddress, snap, func(p *portExt) []ports.AddressPair { return p.AllowedAddressPairs }, add)
		if err != nil {
			return false, err
		}
		opts.AllowedAddressPairs = &pairs
		changed = true
	}
	if f.noBindingProfile || len(f.bindingProfile) > 0 {
		profile, err := portSetBindingProfile(f, snap)
		if err != nil {
			return false, err
		}
		attrs["binding:profile"] = profile
		changed = true
	}
	return changed, nil
}

// portSetBindingProfile merges --binding-profile into the port's current
// profile, or into an empty one under --no-binding-profile.
func portSetBindingProfile(f *portSetFlags, snap *portSnapshot) (map[string]any, error) {
	add, err := parseBindingProfile(f.bindingProfile)
	if err != nil {
		return nil, err
	}
	profile := map[string]any{}
	if !f.noBindingProfile {
		p, err := snap.get()
		if err != nil {
			return nil, err
		}
		maps.Copy(profile, p.BindingProfile)
	}
	maps.Copy(profile, add)
	return profile, nil
}
