package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/external"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/mtu"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/provider"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNetworkCommand builds "network ...".
func newNetworkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage networks",
	}
	cmd.AddCommand(newNetworkListCommand(a, o))
	cmd.AddCommand(newNetworkShowCommand(a, o))
	cmd.AddCommand(newNetworkCreateCommand(a, o))
	cmd.AddCommand(newNetworkDeleteCommand(a, o))
	cmd.AddCommand(newNetworkSetCommand(a, o))
	cmd.AddCommand(newNetworkUnsetCommand(a, o))
	return cmd
}

// MTUExt carries the mtu extension attribute, absent from the base
// networks.Network struct. It is an exported embedded struct so gophercloud's
// reflection-based extractIntoPtr (which calls .Interface() on every embedded
// struct field) can decode it; a bare or unexported field would be either
// shadowed by the promoted Network.UnmarshalJSON or rejected by reflect.
type MTUExt struct {
	MTU int `json:"mtu"`
}

// networkExt is a Network decorated with the mtu, external-net, provider and
// the remaining extension attributes (NetExtAttrs). Each is an anonymous
// embed so ExtractInto populates them all — the standard gophercloud "network
// with extensions" pattern.
type networkExt struct {
	networks.Network
	external.NetworkExternalExt
	provider.NetworkProviderExt
	MTUExt
	NetExtAttrs
}

// networkShowFields renders the attributes upstream's "network show" prints,
// under the same keys (is_vlan_transparent and is_vlan_qinq are the SDK's
// names for vlan_transparent and qinq).
func networkShowFields(n *networkExt) ([]string, []any) {
	fields := []string{
		"id", "name", "status", "admin_state_up", "shared", "router:external",
		"is_default", "mtu", "subnets", fieldProviderNetworkType,
		fieldProviderPhysicalNetwork, netAttrSegmentationID, netAttrPortSecurity,
		netAttrQoSPolicyID, netAttrDNSDomain, "is_vlan_transparent", "is_vlan_qinq", netAttrPVLAN,
		"ipv4_address_scope", "ipv6_address_scope",
		"availability_zone_hints", "availability_zones", "description",
		"project_id", "tags", "revision_number", "created_at", "updated_at",
	}
	values := []any{
		n.ID, n.Name, n.Status, n.AdminStateUp, n.Shared, n.External,
		n.IsDefault, n.MTU, n.Subnets, n.NetworkType,
		n.PhysicalNetwork, n.SegmentationID, n.PortSecurityEnabled,
		n.QoSPolicyID, n.DNSDomain, n.VLANTransparent, n.QinQ, n.PVLAN,
		n.IPv4AddressScope, n.IPv6AddressScope,
		n.AvailabilityZoneHints, n.AvailabilityZones, n.Description,
		n.ProjectID, n.Tags, n.RevisionNumber, n.CreatedAt, n.UpdatedAt,
	}
	return fields, values
}

func newNetworkListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &networkListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List networks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			f.externalSet = fl.Changed(netFlagExternal)
			if err := mutuallyExclusive(fl, flagShare, flagNoShare); err != nil {
				return err
			}
			f.shared = enableDisable(fl, f.share, f.noShare, flagShare, flagNoShare)
			f.adminState = enableDisable(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runNetworkList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.BoolVar(&f.external, netFlagExternal, false, "list only external networks (use --external=false for internal)")
	fl.BoolVar(&f.internal, netFlagInternal, false, "list only internal networks")
	fl.StringVar(&f.name, "name", "", "list networks matching this name")
	fl.BoolVar(&f.enable, "enable", false, "list only enabled networks")
	fl.BoolVar(&f.disable, "disable", false, "list only disabled networks")
	fl.StringVar(&f.project, flagProject, "", "list networks owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.StringVar(&f.status, "status", "", "list networks with this status (ACTIVE, DOWN, BUILD, ERROR)")
	fl.BoolVar(&f.share, flagShare, false, "list only shared networks")
	fl.BoolVar(&f.noShare, flagNoShare, false, "list only non-shared networks")
	fl.StringVar(&f.providerNetworkType, netFlagProviderType, "", "list networks of this provider network type (flat, vlan, vxlan, ...)")
	fl.StringVar(&f.providerPhysicalNetwork, netFlagProviderPhysNet, "", "list networks on this provider physical network")
	fl.StringVar(&f.providerSegment, netFlagProviderSegment, "", "list networks with this provider segmentation ID")
	fl.StringVar(&f.agent, "agent", "", "list only networks hosted by this DHCP agent (ID only; other filters are ignored, as upstream)")
	fl.BoolVar(&f.pvlan, netFlagPVLAN, false, "list only networks with private VLAN enabled (requires the pvlan extension)")
	fl.BoolVar(&f.noPVLAN, netFlagNoPVLAN, false, "list only networks with private VLAN disabled (requires the pvlan extension)")
	bindTagFilterFlags(fl, &f.tagFilterFlags, "networks")
	cmd.MarkFlagsMutuallyExclusive(netFlagExternal, netFlagInternal)
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive(netFlagPVLAN, netFlagNoPVLAN)
	return cmd
}

type networkListFlags struct {
	long        bool
	external    bool
	externalSet bool
	internal    bool
	name        string
	enable      bool
	disable     bool

	project       string
	projectDomain string
	status        string
	share         bool
	noShare       bool

	providerNetworkType     string
	providerPhysicalNetwork string
	providerSegment         string

	agent   string
	pvlan   bool
	noPVLAN bool
	tagFilterFlags

	shared     *bool
	adminState *bool
}

// routerExternal is the router:external filter: --internal means false,
// --external (or --external=false) means what it says, neither means unset.
func (f *networkListFlags) routerExternal() *bool {
	switch {
	case f.internal:
		return boolPtr(false)
	case f.externalSet:
		return boolPtr(f.external)
	default:
		return nil
	}
}

// providerListOptsExt adds the provider-extension query parameters neutron
// accepts on GET /networks. gophercloud's provider package has CreateOptsExt and
// UpdateOptsExt but no ListOptsExt at v2.13.0, and networks.ListOpts has no
// fields for them, so this local extension follows the same composition pattern
// as external.ListOptsExt — including wrapping it, so --external and
// --provider-* work together.
type providerListOptsExt struct {
	networks.ListOptsBuilder
	NetworkType     string
	PhysicalNetwork string
	SegmentationID  string
}

func (opts providerListOptsExt) ToNetworkListQuery() (string, error) {
	q, err := opts.ListOptsBuilder.ToNetworkListQuery()
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(q)
	if err != nil {
		return "", err
	}
	params := parsed.Query()
	for key, value := range map[string]string{
		fieldProviderNetworkType:     opts.NetworkType,
		fieldProviderPhysicalNetwork: opts.PhysicalNetwork,
		"provider:segmentation_id":   opts.SegmentationID,
	} {
		if value != "" {
			params.Add(key, value)
		}
	}
	return (&url.URL{RawQuery: params.Encode()}).String(), nil
}

// networkListQueryExt appends filters networks.ListOpts does not model.
type networkListQueryExt struct {
	networks.ListOptsBuilder
	extra url.Values
}

func (opts networkListQueryExt) ToNetworkListQuery() (string, error) {
	q, err := opts.ListOptsBuilder.ToNetworkListQuery()
	return withQueryValues(q, err, opts.extra)
}

func (f *networkListFlags) hasProviderFilter() bool {
	return f.providerNetworkType != "" || f.providerPhysicalNetwork != "" || f.providerSegment != ""
}

func runNetworkList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *networkListFlags, projectID string, w io.Writer,
) error {
	if f.agent != "" {
		return runNetworkListByAgent(ctx, client, o, f.agent, w)
	}
	base := networks.ListOpts{
		Name:         f.name,
		ProjectID:    projectID,
		Status:       f.status,
		Shared:       f.shared,
		AdminStateUp: f.adminState,
	}
	f.apply(&base.Tags, &base.TagsAny, &base.NotTags, &base.NotTagsAny)
	var opts networks.ListOptsBuilder = base
	if ext := f.routerExternal(); ext != nil {
		opts = external.ListOptsExt{ListOptsBuilder: opts, External: ext}
	}
	if f.hasProviderFilter() {
		opts = providerListOptsExt{
			ListOptsBuilder: opts,
			NetworkType:     f.providerNetworkType,
			PhysicalNetwork: f.providerPhysicalNetwork,
			SegmentationID:  f.providerSegment,
		}
	}
	// pvlan is a server-side filter (is_filter in neutron-lib's pvlan
	// definition). Upstream passes it to openstacksdk, which has no query
	// mapping for it and filters the fetched list client-side instead; the
	// server-side filter returns the same networks without fetching the rest,
	// and a cloud without the extension rejects it (named below) rather than
	// answering with an empty list.
	var filterAttrs map[string]any
	if pv := pairBool(f.pvlan, f.noPVLAN); pv != nil {
		opts = networkListQueryExt{ListOptsBuilder: opts, extra: url.Values{netAttrPVLAN: {strconv.FormatBool(*pv)}}}
		filterAttrs = map[string]any{netAttrPVLAN: *pv}
	}
	pages, err := networks.List(client, opts).AllPages(ctx)
	if err != nil {
		return explainMissingExtension(ctx, client, fmt.Errorf("listing networks: %w", err), filterAttrs)
	}
	var all []networkExt
	if err := networks.ExtractNetworksInto(pages, &all); err != nil {
		return fmt.Errorf("parsing network list: %w", err)
	}
	return o.WriteList(w, networkListTable(all, f.long))
}

// runNetworkListByAgent is "network list --agent": the networks a DHCP agent
// hosts (GET /agents/{id}/dhcp-networks). Upstream ignores every other filter
// and --long on this path and prints the short columns; so does koc.
func runNetworkListByAgent(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, agentID string, w io.Writer) error {
	var all []networkExt
	if err := agents.ListDHCPNetworks(ctx, client, agentID).ExtractIntoSlicePtr(&all, "networks"); err != nil {
		return fmt.Errorf("listing networks hosted by DHCP agent %s: %w", agentID, err)
	}
	return o.WriteList(w, networkListTable(all, false))
}

// networkListTable renders upstream ListNetwork's columns: ID, Name and
// Subnets by default; --long adds status, project, state, shared, network
// type, router type, availability zones and tags.
func networkListTable(list []networkExt, long bool) output.Table {
	cols := []string{"ID", "Name", "Subnets"}
	if long {
		cols = []string{"ID", "Name", "Status", "Project", "State", "Shared", "Subnets",
			"Network Type", "Router Type", "Availability Zones", "Tags"}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for i := range list {
		n := &list[i]
		if long {
			t.Rows = append(t.Rows, []any{n.ID, n.Name, n.Status, n.ProjectID, adminState(n.AdminStateUp), n.Shared,
				n.Subnets, n.NetworkType, routerType(n.External), n.AvailabilityZones, n.Tags})
		} else {
			t.Rows = append(t.Rows, []any{n.ID, n.Name, n.Subnets})
		}
	}
	return t
}

func adminState(up bool) string {
	if up {
		return "UP"
	}
	return "DOWN"
}

// routerType is upstream's RouterExternalColumn.
func routerType(external bool) string {
	if external {
		return "External"
	}
	return "Internal"
}

func newNetworkShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <network>",
		Short: "Show details of a network",
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
			return runNetworkShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNetworkShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveNetworkID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	var n networkExt
	if err := networks.Get(ctx, client, id).ExtractInto(&n); err != nil {
		return fmt.Errorf("getting network %s: %w", nameOrID, err)
	}
	fields, values := networkShowFields(&n)
	return o.WriteSingle(w, fields, values)
}

// networkCreateFlags mirrors upstream CreateNetwork (network/v2/network.py).
// Each on/off pair is mutually exclusive, as upstream's argparse groups are.
// --pvlan/--no-pvlan and --qinq-vlan/--no-qinq-vlan are post-Zed: their help
// names the extension, and a cloud without it gets the extension named in the
// error (explainMissingExtension).
type networkCreateFlags struct {
	enable              bool
	disable             bool
	share               bool
	noShare             bool
	external            bool
	internal            bool
	defaultNet          bool
	noDefault           bool
	enablePortSecurity  bool
	disablePortSecurity bool
	transparentVLAN     bool
	noTransparentVLAN   bool
	qinqVLAN            bool
	noQinQVLAN          bool
	pvlan               bool
	noPVLAN             bool
	mtu                 int
	providerType        string
	providerPhysNet     string
	providerSegment     string
	description         string
	qosPolicy           string
	dnsDomain           string
	azHints             []string
	project             string
	projectDomain       string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	extraProperty []string
	tagWriteFlags
}

func newNetworkCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &networkCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new network",
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
			return runNetworkCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the network (admin state up, default)")
	fl.BoolVar(&f.disable, "disable", false, "disable the network (admin state down)")
	fl.BoolVar(&f.share, flagShare, false, "share the network across projects")
	fl.BoolVar(&f.noShare, flagNoShare, false, "do not share the network across projects")
	fl.BoolVar(&f.external, netFlagExternal, false, "set the network as external (router:external)")
	fl.BoolVar(&f.internal, netFlagInternal, false, "set the network as internal (default)")
	fl.BoolVar(&f.defaultNet, flagDefault, false, "use the network as the default external network")
	fl.BoolVar(&f.noDefault, flagNoDefault, false, "do not use the network as the default external network (default)")
	fl.BoolVar(&f.enablePortSecurity, flagEnablePortSecurity, false, "enable port security by default for ports on this network (default)")
	fl.BoolVar(&f.disablePortSecurity, flagDisablePortSecurity, false, "disable port security by default for ports on this network")
	fl.BoolVar(&f.transparentVLAN, netFlagTransparentVLAN, false, "make the network VLAN transparent")
	fl.BoolVar(&f.noTransparentVLAN, netFlagNoTransparentVLAN, false, "do not make the network VLAN transparent")
	fl.BoolVar(&f.qinqVLAN, netFlagQinQVLAN, false,
		"enable VLAN QinQ (S-tag ethertype 0x88a8) for the network (requires the qinq extension)")
	fl.BoolVar(&f.noQinQVLAN, netFlagNoQinQVLAN, false, "disable VLAN QinQ for the network (requires the qinq extension)")
	fl.BoolVar(&f.pvlan, netFlagPVLAN, false, "enable private VLAN for the network (requires the pvlan extension)")
	fl.BoolVar(&f.noPVLAN, netFlagNoPVLAN, false, "disable private VLAN for the network (requires the pvlan extension)")
	fl.IntVar(&f.mtu, "mtu", 0, "maximum transmission unit for the network")
	fl.StringVar(&f.providerType, netFlagProviderType, "", "physical network type (flat, vlan, vxlan, ...)")
	fl.StringVar(&f.providerPhysNet, netFlagProviderPhysNet, "", "name of the physical network")
	fl.StringVar(&f.providerSegment, netFlagProviderSegment, "", "VLAN ID or tunnel ID for the network segment (requires --provider-network-type)")
	fl.StringVar(&f.description, flagDescription, "", "description for the network")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy to attach to the network (name or ID)")
	fl.StringVar(&f.dnsDomain, flagDNSDomain, "", "DNS domain for the network (requires the dns-integration extension)")
	fl.StringArrayVar(&f.azHints, flagAvailabilityZoneHint, nil, "availability zone to create the network in (repeatable)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagCreateFlags(cmd, &f.tagWriteFlags, "network")
	for _, pair := range [][2]string{
		{"enable", "disable"},
		{flagShare, flagNoShare},
		{netFlagExternal, netFlagInternal},
		{flagDefault, flagNoDefault},
		{flagEnablePortSecurity, flagDisablePortSecurity},
		{netFlagTransparentVLAN, netFlagNoTransparentVLAN},
		{netFlagQinQVLAN, netFlagNoQinQVLAN},
		{netFlagPVLAN, netFlagNoPVLAN},
	} {
		cmd.MarkFlagsMutuallyExclusive(pair[0], pair[1])
	}
	return cmd
}

// providerCreateOpts injects the single-value provider extension attributes
// into a network create body. Gophercloud's provider.CreateOptsExt only
// supports the multi-segment form, so these top-level keys are set manually.
type providerCreateOpts struct {
	networks.CreateOptsBuilder
	NetworkType     string
	PhysicalNetwork string
	SegmentationID  string
}

func (opts providerCreateOpts) ToNetworkCreateMap() (map[string]any, error) {
	base, err := opts.CreateOptsBuilder.ToNetworkCreateMap()
	if err != nil {
		return nil, err
	}
	m, ok := base["network"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("providerCreateOpts: unexpected \"network\" body shape %T", base["network"])
	}
	if opts.NetworkType != "" {
		m[fieldProviderNetworkType] = opts.NetworkType
	}
	if opts.PhysicalNetwork != "" {
		m[fieldProviderPhysicalNetwork] = opts.PhysicalNetwork
	}
	if opts.SegmentationID != "" {
		m["provider:segmentation_id"] = opts.SegmentationID
	}
	return base, nil
}

func runNetworkCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *networkCreateFlags, w io.Writer) error {
	if f.providerSegment != "" && f.providerType == "" {
		return fmt.Errorf("--%s requires --%s", netFlagProviderSegment, netFlagProviderType)
	}
	// admin_state_up is always sent (upstream's --enable defaults to true).
	base := networks.CreateOpts{
		Name:                  name,
		Description:           f.description,
		AdminStateUp:          boolPtr(!f.disable),
		Shared:                pairBool(f.share, f.noShare),
		ProjectID:             f.projectID,
		AvailabilityZoneHints: f.azHints,
	}

	var builder networks.CreateOptsBuilder = base
	if f.mtu > 0 {
		builder = mtu.CreateOptsExt{CreateOptsBuilder: builder, MTU: f.mtu}
	}
	// Upstream sends router:external only when --external or --internal is given;
	// is_default is sent as given, without checking --external (neutron does).
	if ext := pairBool(f.external, f.internal); ext != nil {
		builder = external.CreateOptsExt{CreateOptsBuilder: builder, External: ext}
	}
	if f.providerType != "" || f.providerPhysNet != "" || f.providerSegment != "" {
		builder = providerCreateOpts{
			CreateOptsBuilder: builder,
			NetworkType:       f.providerType,
			PhysicalNetwork:   f.providerPhysNet,
			SegmentationID:    f.providerSegment,
		}
	}
	attrs, err := networkCreateAttrs(ctx, client, f)
	if err != nil {
		return err
	}

	var n networkExt
	if err := networks.Create(ctx, client, withNetworkCreateAttrs(builder, attrs)).ExtractInto(&n); err != nil {
		return explainMissingExtension(ctx, client, fmt.Errorf("creating network: %w", err), attrs)
	}
	// Tags cannot ride on the create; upstream sets them afterwards too.
	if n.Tags, err = applyTagsForSet(ctx, client, tagResourceNetworks, n.ID, n.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := networkShowFields(&n)
	return o.WriteSingle(w, fields, values)
}

func newNetworkDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <network> [<network> ...]",
		Short: "Delete network(s)",
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
			return runNetworkDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNetworkDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	return batchdelete.Each(names, func(nameOrID string) error {
		id, err := resolveNetworkID(ctx, client, nameOrID)
		if err != nil {
			return err
		}
		if err := networks.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting network %s: %w", nameOrID, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted network %s\n", nameOrID); err != nil {
			return err
		}
		return nil
	})
}

// networkSetFlags mirrors upstream SetNetwork. --pvlan/--no-pvlan are
// post-Zed; --qinq-vlan is create-only upstream (and allow_put is false in
// neutron-lib's qinq definition), so set has no QinQ pair.
type networkSetFlags struct {
	name                string
	description         string
	mtu                 int
	enable              bool
	disable             bool
	share               bool
	noShare             bool
	external            bool
	internal            bool
	defaultNet          bool
	noDefault           bool
	enablePortSecurity  bool
	disablePortSecurity bool
	qosPolicy           string
	noQoSPolicy         bool
	pvlan               bool
	noPVLAN             bool
	dnsDomain           string
	providerType        string
	providerPhysNet     string
	providerSegment     string
	extraProperty       []string
	tagWriteFlags
}

func newNetworkSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &networkSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <network>",
		Short: "Set network properties",
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
			return runNetworkSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new network name")
	fl.StringVar(&f.description, flagDescription, "", "new description for the network")
	fl.IntVar(&f.mtu, "mtu", 0, "new maximum transmission unit")
	fl.BoolVar(&f.enable, "enable", false, "enable the network (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "disable the network (admin state down)")
	fl.BoolVar(&f.share, flagShare, false, "share the network across projects")
	fl.BoolVar(&f.noShare, flagNoShare, false, "stop sharing the network across projects")
	fl.BoolVar(&f.external, netFlagExternal, false, "set the network as external (router:external)")
	fl.BoolVar(&f.internal, netFlagInternal, false, "set the network as internal")
	fl.BoolVar(&f.defaultNet, flagDefault, false, "use the network as the default external network")
	fl.BoolVar(&f.noDefault, flagNoDefault, false, "do not use the network as the default external network")
	fl.BoolVar(&f.enablePortSecurity, flagEnablePortSecurity, false, "enable port security by default for ports on this network")
	fl.BoolVar(&f.disablePortSecurity, flagDisablePortSecurity, false, "disable port security by default for ports on this network")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy to attach to the network (name or ID)")
	fl.BoolVar(&f.noQoSPolicy, flagNoQoSPolicy, false, "detach the network's QoS policy")
	fl.BoolVar(&f.pvlan, netFlagPVLAN, false, "enable private VLAN for the network (requires the pvlan extension)")
	fl.BoolVar(&f.noPVLAN, netFlagNoPVLAN, false, "disable private VLAN for the network (requires the pvlan extension)")
	fl.StringVar(&f.dnsDomain, flagDNSDomain, "", "DNS domain for the network (requires the dns-integration extension)")
	fl.StringVar(&f.providerType, netFlagProviderType, "", "physical network type (flat, vlan, vxlan, ...)")
	fl.StringVar(&f.providerPhysNet, netFlagProviderPhysNet, "", "name of the physical network")
	fl.StringVar(&f.providerSegment, netFlagProviderSegment, "", "VLAN ID or tunnel ID for the network segment")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagSetFlags(fl, &f.tagWriteFlags, "network")
	for _, pair := range [][2]string{
		{flagShare, flagNoShare},
		{netFlagExternal, netFlagInternal},
		{flagDefault, flagNoDefault},
		{flagEnablePortSecurity, flagDisablePortSecurity},
		{flagQoSPolicy, flagNoQoSPolicy},
		{netFlagPVLAN, netFlagNoPVLAN},
	} {
		cmd.MarkFlagsMutuallyExclusive(pair[0], pair[1])
	}
	return cmd
}

func runNetworkSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *networkSetFlags, flags flagSet, w io.Writer) error {
	if err := mutuallyExclusive(flags, "enable", "disable"); err != nil {
		return err
	}
	id, err := resolveNetworkID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts, changed := networkSetOpts(f, flags)
	attrs, err := networkSetAttrs(ctx, client, f, flags)
	if err != nil {
		return err
	}
	changed = changed || len(attrs) > 0
	if !changed && !f.given() {
		return fmt.Errorf("network set requires at least one attribute flag")
	}
	req := networkUpdate{opts: opts, attrs: attrs, changed: changed, tags: tagEdit{applyTagsForSet, &f.tagWriteFlags}}
	return updateNetwork(ctx, client, o, nameOrID, id, req, w)
}

// flagSet is the small surface of *pflag.FlagSet used by the set/unset seams,
// kept as an interface so tests can drive runNetworkSet without cobra.
type flagSet interface {
	Changed(string) bool
}

type networkUnsetFlags struct {
	share         bool
	extraProperty []string
	tagWriteFlags
}

func newNetworkUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &networkUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <network>",
		Short: "Unset network properties",
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
			return runNetworkUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// koc-only extra: upstream spells this "network set --no-share".
	fl.BoolVar(&f.share, flagShare, false, "make the network project-private (unset shared)")
	bindExtraPropertyUnsetFlag(fl, &f.extraProperty)
	bindTagUnsetFlags(cmd, &f.tagWriteFlags, "network")
	return cmd
}

func runNetworkUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *networkUnsetFlags, w io.Writer) error {
	id, err := resolveNetworkID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	var opts networks.UpdateOpts
	if f.share {
		opts.Shared = boolPtr(false)
	}
	attrs, err := parseExtraProperties(f.extraProperty, true)
	if err != nil {
		return err
	}
	changed := f.share || len(attrs) > 0
	if !changed && !f.given() {
		return fmt.Errorf("network unset requires at least one attribute flag")
	}
	req := networkUpdate{opts: opts, attrs: attrs, changed: changed, tags: tagEdit{applyTagsForUnset, &f.tagWriteFlags}}
	return updateNetwork(ctx, client, o, nameOrID, id, req, w)
}
