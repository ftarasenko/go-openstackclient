package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

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
		"description", "project_id", "tags", "created_at", "updated_at",
	}
	// port_security_enabled stays nil when the deployment does not run the
	// extension, so it renders empty rather than a misleading "false".
	var portSecurity any
	if p.PortSecurityEnabled != nil {
		portSecurity = *p.PortSecurityEnabled
	}
	values := []any{
		p.ID, p.Name, p.NetworkID, p.MACAddress, p.Status, p.AdminStateUp,
		p.DeviceOwner, p.DeviceID, formatFixedIPs(p.FixedIPs), p.SecurityGroups,
		formatAddressPairs(p.AllowedAddressPairs), portSecurity,
		p.Description, p.ProjectID, p.Tags, p.CreatedAt, p.UpdatedAt,
	}
	return fields, values
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
	tags          []string
	anyTags       []string
	notTags       []string
	notAnyTags    []string
	long          bool
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
	fl.StringVar(&f.host, "host", "", "list only ports bound to this host ID")
	fl.StringVar(&f.macAddress, flagMACAddress, "", "list only ports with this MAC address")
	fl.StringVar(&f.status, "status", "", "list only ports with this status (ACTIVE, BUILD, DOWN, ERROR)")
	fl.StringVar(&f.project, "project", "", "list only ports in this project (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project (name or ID)")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "list only ports in this security group (name or ID, repeatable)")
	// OSC form: --fixed-ip subnet=<subnet>,ip-address=<ip>,ip-substring=<substr>; repeatable.
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil, "filter by fixed IP: subnet=/ip-address=/ip-substring= pairs; repeatable")
	fl.StringSliceVar(&f.tags, "tags", nil, "list only ports with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.anyTags, "any-tags", nil, "list only ports with any of these tags (comma-separated)")
	fl.StringSliceVar(&f.notTags, "not-tags", nil, "exclude ports with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.notAnyTags, "not-any-tags", nil, "exclude ports with any of these tags (comma-separated)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	// Upstream OSC models these three as one device filter; they all set device_id.
	cmd.MarkFlagsMutuallyExclusive("router", "server", "device-id")
	return cmd
}

// portListOpts adds the query parameters neutron accepts but gophercloud's
// ports.ListOpts does not model — binding:host_id, behind --host.
type portListOpts struct {
	ports.ListOpts
	hostID string
}

func (opts portListOpts) ToPortListQuery() (string, error) {
	q, err := opts.ListOpts.ToPortListQuery()
	if err != nil || opts.hostID == "" {
		return q, err
	}
	params, err := url.ParseQuery(strings.TrimPrefix(q, "?"))
	if err != nil {
		return "", fmt.Errorf("building port list query: %w", err)
	}
	params.Set("binding:host_id", opts.hostID)
	return "?" + params.Encode(), nil
}

// portExt is a Port decorated with the trunk_details attribute, which
// gophercloud does not model; `port list --long` renders its sub_ports as
// "Trunk subports". Both parts are anonymous embeds so ExtractPortsInto
// populates them — that extraction path only decodes into struct-kind fields, so
// the attribute has to arrive via a flat extension struct (as with MTUExt on
// networkExt) rather than a named pointer field.
type portExt struct {
	ports.Port
	TrunkDetailsExt
	PortSecurityExt
}

// PortSecurityExt carries the port_security_enabled attribute, which
// gophercloud's ports.Port does not model. `port set
// --enable/--disable-port-security` wrote it (see portUpdateOptsExt) but nothing
// could read it back, so the flag was write-only and its effect unverifiable
// from koc. Exported and flat for the same reason as TrunkDetailsExt.
type PortSecurityExt struct {
	PortSecurityEnabled *bool `json:"port_security_enabled"`
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
			Tags:        strings.Join(f.tags, ","),
			TagsAny:     strings.Join(f.anyTags, ","),
			NotTags:     strings.Join(f.notTags, ","),
			NotTagsAny:  strings.Join(f.notAnyTags, ","),
		},
		hostID: f.host,
	}
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
	return o.WriteList(w, portListTable(all, f.long))
}

func portListTable(list []portExt, long bool) output.Table {
	cols := []string{"ID", "Name", "MAC Address", "Fixed IP Addresses", "Status"}
	if long {
		cols = append(cols, "Security Groups", "Device Owner", "Tags", "Trunk subports")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for i := range list {
		p := &list[i]
		row := []any{p.ID, p.Name, p.MACAddress, formatFixedIPs(p.FixedIPs), p.Status}
		if long {
			row = append(row, p.SecurityGroups, p.DeviceOwner, p.Tags, p.TrunkDetails.SubPorts)
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

type portCreateFlags struct {
	network             string
	fixedIP             []string
	macAddress          string
	deviceOwner         string
	description         string
	securityGroup       []string
	noSecurityGroup     bool
	allowedAddress      []string
	enablePortSecurity  bool
	disablePortSecurity bool
	enable              bool
	disable             bool
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
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortCreate(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.network, "network", "", "network for the port (name or ID, required)")
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil, "desired IP as subnet=<name|id>,ip-address=<ip> (repeatable)")
	fl.StringVar(&f.macAddress, flagMACAddress, "", "MAC address for the port")
	fl.StringVar(&f.deviceOwner, flagDeviceOwner, "", "device owner for the port")
	fl.StringVar(&f.description, "description", "", "description for the port")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "security group to associate (name or ID, repeatable)")
	fl.BoolVar(&f.noSecurityGroup, flagNoSecurityGroup, false, "create the port with no security groups")
	fl.StringArrayVar(&f.allowedAddress, flagAllowedAddress, nil, "allowed address pair as ip-address=<ip>[,mac-address=<mac>] (repeatable)")
	fl.BoolVar(&f.enablePortSecurity, flagEnablePortSecurity, false, "enable port security (security groups and anti-spoofing)")
	fl.BoolVar(&f.disablePortSecurity, flagDisablePortSecurity, false, "disable port security")
	fl.BoolVar(&f.enable, "enable", false, "create the port administratively up (default)")
	fl.BoolVar(&f.disable, "disable", false, "create the port administratively down")
	cmd.MarkFlagsMutuallyExclusive(flagSecurityGroup, flagNoSecurityGroup)
	cmd.MarkFlagsMutuallyExclusive(flagEnablePortSecurity, flagDisablePortSecurity)
	_ = cmd.MarkFlagRequired("network")
	return cmd
}

func runPortCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *portCreateFlags, flags flagSet, w io.Writer) error {
	networkID, err := resolveNetworkID(ctx, client, f.network)
	if err != nil {
		return err
	}
	opts := ports.CreateOpts{
		NetworkID:   networkID,
		Name:        name,
		MACAddress:  f.macAddress,
		DeviceOwner: f.deviceOwner,
		Description: f.description,
	}
	switch {
	case f.disable:
		opts.AdminStateUp = boolPtr(false)
	case f.enable:
		opts.AdminStateUp = boolPtr(true)
	}
	fixedIPs, err := buildFixedIPs(ctx, client, f.fixedIP)
	if err != nil {
		return err
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
			return err
		}
		opts.SecurityGroups = &sgIDs
	}
	if len(f.allowedAddress) > 0 {
		pairs, err := parseAddressPairs(f.allowedAddress)
		if err != nil {
			return err
		}
		opts.AllowedAddressPairs = pairs
	}

	// port_security_enabled lives in neutron's port-security extension, which
	// gophercloud v2.13.0 has no create-side package for, so it is layered on
	// the same way runPortSet does it.
	var builder ports.CreateOptsBuilder = opts
	if secure := enableDisable(flags, f.enablePortSecurity, f.disablePortSecurity,
		flagEnablePortSecurity, flagDisablePortSecurity); secure != nil {
		builder = portCreateOptsExt{CreateOptsBuilder: opts, PortSecurityEnabled: secure}
	}

	var p portExt
	if err := ports.Create(ctx, client, builder).ExtractInto(&p); err != nil {
		return fmt.Errorf("creating port: %w", err)
	}
	fields, values := portShowFields(&p)
	return o.WriteSingle(w, fields, values)
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

type portSetFlags struct {
	name            string
	fixedIP         []string
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
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new port name")
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil, "desired IP as subnet=<name|id>,ip-address=<ip> (repeatable, replaces existing)")
	fl.StringVar(&f.description, "description", "", "new port description")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "security group to associate (name or ID, repeatable, replaces existing)")
	fl.BoolVar(&f.noSecurityGroup, flagNoSecurityGroup, false, "clear all security groups from the port")
	fl.BoolVar(&f.enable, "enable", false, "set the port administratively up")
	fl.BoolVar(&f.disable, "disable", false, "set the port administratively down")
	fl.StringArrayVar(&f.allowedAddress, flagAllowedAddress, nil,
		"allowed address pair as ip-address=<ip>[,mac-address=<mac>] (repeatable, replaces existing)")
	fl.BoolVar(&f.noAllowedAddress, flagNoAllowedAddress, false, "clear all allowed address pairs from the port")
	fl.BoolVar(&f.enablePortSecurity, flagEnablePortSecurity, false, "enable port security (security groups and anti-spoofing)")
	fl.BoolVar(&f.disablePortSecurity, flagDisablePortSecurity, false, "disable port security")
	fl.StringVar(&f.host, "host", "", "binding host ID for the port")
	fl.StringVar(&f.device, "device", "", "device ID the port is attached to")
	fl.StringVar(&f.deviceOwner, flagDeviceOwner, "", "device owner of the port")
	cmd.MarkFlagsMutuallyExclusive(flagSecurityGroup, flagNoSecurityGroup)
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive(flagAllowedAddress, flagNoAllowedAddress)
	cmd.MarkFlagsMutuallyExclusive(flagEnablePortSecurity, flagDisablePortSecurity)
	return cmd
}

func runPortSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *portSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolvePortID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts := ports.UpdateOpts{}
	changed := false
	if f.name != "" {
		opts.Name = &f.name
		changed = true
	}
	if flags.Changed("description") {
		opts.Description = &f.description
		changed = true
	}
	if flags.Changed(flagFixedIP) {
		fixedIPs, err := buildFixedIPs(ctx, client, f.fixedIP)
		if err != nil {
			return err
		}
		opts.FixedIPs = fixedIPs
		changed = true
	}
	if state := enableDisable(flags, f.enable, f.disable); state != nil {
		opts.AdminStateUp = state
		changed = true
	}
	switch {
	case f.noSecurityGroup:
		opts.SecurityGroups = &[]string{}
		changed = true
	case flags.Changed(flagSecurityGroup):
		sgIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return err
		}
		opts.SecurityGroups = &sgIDs
		changed = true
	}
	switch {
	case f.noAllowedAddress:
		opts.AllowedAddressPairs = &[]ports.AddressPair{}
		changed = true
	case flags.Changed(flagAllowedAddress):
		pairs, err := parseAddressPairs(f.allowedAddress)
		if err != nil {
			return err
		}
		opts.AllowedAddressPairs = &pairs
		changed = true
	}
	if flags.Changed("device") {
		opts.DeviceID = &f.device
		changed = true
	}
	if flags.Changed(flagDeviceOwner) {
		opts.DeviceOwner = &f.deviceOwner
		changed = true
	}

	// --host and --*-port-security live in neutron's portsbinding and
	// port-security extensions, which gophercloud v2.13.0 does not vendor a
	// package for, so they are layered on as a local UpdateOptsBuilder.
	ext := portUpdateOptsExt{UpdateOptsBuilder: opts}
	if flags.Changed("host") {
		ext.HostID = &f.host
		changed = true
	}
	if secure := enableDisable(flags, f.enablePortSecurity, f.disablePortSecurity,
		flagEnablePortSecurity, flagDisablePortSecurity); secure != nil {
		ext.PortSecurityEnabled = secure
		changed = true
	}

	if !changed {
		return fmt.Errorf("port set requires at least one attribute flag")
	}
	var p portExt
	if err := ports.Update(ctx, client, id, ext).ExtractInto(&p); err != nil {
		return fmt.Errorf("updating port %s: %w", nameOrID, err)
	}
	fields, values := portShowFields(&p)
	return o.WriteSingle(w, fields, values)
}

// portUpdateOptsExt layers the binding and port-security attributes onto a
// ports.UpdateOpts. gophercloud has extensions/portsbinding and
// extensions/portsecurity upstream, but neither is vendored at v2.13.0 and
// ports.UpdateOpts has no fields for them — so rather than pull two packages in
// for two keys, this follows the same composition pattern as
// external.UpdateOptsExt and injects them into the request body. Swap it for the
// vendored extensions if they are ever needed more widely.
// portCreateOptsExt carries port_security_enabled onto a create, which the
// port-security extension defines and gophercloud v2.13.0 does not vendor.
type portCreateOptsExt struct {
	ports.CreateOptsBuilder
	PortSecurityEnabled *bool
}

func (opts portCreateOptsExt) ToPortCreateMap() (map[string]any, error) {
	base, err := opts.CreateOptsBuilder.ToPortCreateMap()
	if err != nil {
		return nil, err
	}
	if opts.PortSecurityEnabled == nil {
		return base, nil
	}
	portMap, ok := base["port"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected port create body shape: %T", base["port"])
	}
	portMap["port_security_enabled"] = *opts.PortSecurityEnabled
	return base, nil
}

type portUpdateOptsExt struct {
	ports.UpdateOptsBuilder
	HostID              *string
	PortSecurityEnabled *bool
}

func (opts portUpdateOptsExt) ToPortUpdateMap() (map[string]any, error) {
	base, err := opts.UpdateOptsBuilder.ToPortUpdateMap()
	if err != nil {
		return nil, err
	}
	if opts.HostID == nil && opts.PortSecurityEnabled == nil {
		return base, nil
	}
	portMap, ok := base["port"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected port update body shape: %T", base["port"])
	}
	if opts.HostID != nil {
		portMap["binding:host_id"] = *opts.HostID
	}
	if opts.PortSecurityEnabled != nil {
		portMap["port_security_enabled"] = *opts.PortSecurityEnabled
	}
	return base, nil
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
