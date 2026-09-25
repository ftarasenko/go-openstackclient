package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/siteconnections"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	flagVPNPeerCIDR           = "peer-cidr"
	flagVPNLocalEndpointGroup = "local-endpoint-group"
	flagVPNPeerEndpointGroup  = "peer-endpoint-group"
)

func newIPsecSiteConnectionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "connection", Short: "Manage IPsec site connections"}
	cmd.AddCommand(
		newIPsecSiteConnectionCreateCommand(a, o),
		newVPNDeleteCommand(a, o, "delete <ipsec-site-connection> [<ipsec-site-connection> ...]",
			"Delete IPsec site connection(s)", runIPsecSiteConnectionDelete),
		newVPNListCommand(a, o, "List IPsec site connections", runIPsecSiteConnectionList),
		newIPsecSiteConnectionSetCommand(a, o),
		newVPNShowCommand(a, o, "show <ipsec-site-connection>", "Show IPsec site connection details", runIPsecSiteConnectionShow),
	)
	return cmd
}

func resolveIPsecSiteConnectionID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "IPsec site connection", nameOrID, func(c *gophercloud.ServiceClient) ([]siteconnections.Connection, error) {
		pages, err := siteconnections.List(c, siteconnections.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return siteconnections.ExtractConnections(pages)
	}, func(s siteconnections.Connection) string { return s.ID })
}

// vpnDPD renders a dpd attribute; absent when neutron sent none.
func vpnDPD(d siteconnections.DPD) any {
	if d == (siteconnections.DPD{}) {
		return nil
	}
	return map[string]any{"action": d.Action, "interval": d.Interval, "timeout": d.Timeout}
}

// ipsecSiteConnectionShowFields renders upstream's show columns (display names,
// sorted).
func ipsecSiteConnectionShowFields(s *siteconnections.Connection) ([]string, []any) {
	return []string{
		colAuthAlgorithm, "DPD", "Description", "ID", "IKE Policy",
		"IPSec Policy", "Initiator", "Local Endpoint Group ID", "Local ID", "MTU",
		"Name", "Peer Address", "Peer CIDRs", "Peer Endpoint Group ID", "Peer ID",
		"Pre-shared Key", "Project", "Route Mode", "State", "Status", "VPN Service",
	}, []any{
		s.AuthMode, vpnDPD(s.DPD), s.Description, s.ID, s.IKEPolicyID,
		s.IPSecPolicyID, s.Initiator, s.LocalEPGroupID, s.LocalID, s.MTU,
		s.Name, s.PeerAddress, s.PeerCIDRs, s.PeerEPGroupID, s.PeerID,
		s.PSK, s.ProjectID, s.RouteMode, s.AdminStateUp, s.Status, s.VPNServiceID,
	}
}

func runIPsecSiteConnectionList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	pages, err := siteconnections.List(client, nil).AllPages(ctx)
	if err != nil {
		return explainVPNaaS(ctx, client, fmt.Errorf("listing IPsec site connections: %w", err))
	}
	all, err := siteconnections.ExtractConnections(pages)
	if err != nil {
		return fmt.Errorf("parsing IPsec site connection list: %w", err)
	}
	cols := []string{"ID", "Name", "Peer Address", colAuthAlgorithm, "Status"}
	if long {
		cols = append(cols, "Project", "Peer CIDRs", "VPN Service", "IPSec Policy", "IKE Policy", "MTU",
			"Initiator", "State", "Description", "Pre-shared Key", "Route Mode", "Local ID", "Peer ID",
			"Local Endpoint Group ID", "Peer Endpoint Group ID", "DPD")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		row := []any{s.ID, s.Name, s.PeerAddress, s.AuthMode, s.Status}
		if long {
			row = append(row, s.ProjectID, s.PeerCIDRs, s.VPNServiceID, s.IPSecPolicyID, s.IKEPolicyID, s.MTU,
				s.Initiator, s.AdminStateUp, s.Description, s.PSK, s.RouteMode, s.LocalID, s.PeerID,
				s.LocalEPGroupID, s.PeerEPGroupID, vpnDPD(s.DPD))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func runIPsecSiteConnectionShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	id, err := resolveIPsecSiteConnectionID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := vpnExtracted(siteconnections.Get(ctx, client, id).Extract())
	if err != nil {
		return fmt.Errorf("getting IPsec site connection %s: %w", ref, err)
	}
	fields, values := ipsecSiteConnectionShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func runIPsecSiteConnectionDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return explainVPNaaS(ctx, client, runVPNDelete(ctx, client, "IPsec site connection", refs, resolveIPsecSiteConnectionID,
		func(id string) error { return siteconnections.Delete(ctx, client, id).ExtractErr() }, w))
}

type ipsecSiteConnectionFlags struct {
	name               string
	description        string
	dpd                []string
	mtu                string
	initiator          []*vpnChoice
	peerCIDRs          []string
	localEndpointGroup string
	peerEndpointGroup  string
	enable             bool
	disable            bool
	localID            string
	peerID             string
	peerAddress        string
	// create only
	psk           string
	vpnService    string
	ikePolicy     string
	ipsecPolicy   string
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newIPsecSiteConnectionFlags() *ipsecSiteConnectionFlags {
	return &ipsecSiteConnectionFlags{initiator: []*vpnChoice{
		{flag: "initiator", attr: "initiator", help: "initiator state", choices: []string{"bi-directional", "response-only"}},
	}}
}

// bindIPsecSiteConnectionCommon registers upstream's _get_common_parser flags
// plus --peer-id/--peer-address, which create and set both take.
func bindIPsecSiteConnectionCommon(cmd *cobra.Command, f *ipsecSiteConnectionFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description for the connection")
	fl.StringArrayVar(&f.dpd, "dpd", nil,
		"dead peer detection as action=<action>,interval=<interval>,timeout=<timeout> "+
			"(action: hold, clear, restart, restart-by-peer, disabled; interval/timeout: positive integers)")
	fl.StringVar(&f.mtu, "mtu", "", "MTU size for the connection")
	bindVPNChoices(cmd, f.initiator)
	fl.StringArrayVar(&f.peerCIDRs, flagVPNPeerCIDR, nil,
		"remote subnet in CIDR format (repeatable; not with endpoint groups; needs a VPN service with a subnet)")
	fl.StringVar(&f.localEndpointGroup, flagVPNLocalEndpointGroup, "", "local endpoint group with the subnet(s) (name or ID)")
	fl.StringVar(&f.peerEndpointGroup, flagVPNPeerEndpointGroup, "", "peer endpoint group with the CIDR(s) (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "enable the IPsec site connection")
	fl.BoolVar(&f.disable, "disable", false, "disable the IPsec site connection")
	fl.StringVar(&f.localID, "local-id", "", "ID to use instead of the virtual router's external IP address")
	fl.StringVar(&f.peerID, "peer-id", "", "peer router identity for authentication: IPv4/IPv6 address, e-mail address, key ID or FQDN")
	fl.StringVar(&f.peerAddress, "peer-address", "", "peer gateway public IPv4/IPv6 address or FQDN")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive(flagVPNPeerCIDR, flagVPNLocalEndpointGroup)
}

// ipsecSiteConnectionScalars is the flag-only half of upstream's
// _get_common_attrs, plus peer_id/peer_address/name.
func ipsecSiteConnectionScalars(f *ipsecSiteConnectionFlags) (map[string]any, error) {
	attrs := map[string]any{}
	if f.description != "" {
		attrs["description"] = f.description
	}
	if f.mtu != "" {
		mtu, err := strconv.Atoi(f.mtu)
		if err != nil {
			return nil, fmt.Errorf("--mtu %q: must be an integer", f.mtu)
		}
		attrs["mtu"] = mtu
	}
	vpnEnableDisable(attrs, f.enable, f.disable)
	if err := applyVPNChoices(attrs, f.initiator); err != nil {
		return nil, err
	}
	dpd, err := parseVPNDPD(f.dpd)
	if err != nil {
		return nil, err
	}
	if dpd != nil {
		attrs["dpd"] = dpd
	}
	if len(f.peerCIDRs) > 0 {
		attrs["peer_cidrs"] = f.peerCIDRs
	}
	for attr, v := range map[string]string{"local_id": f.localID, "peer_id": f.peerID, "peer_address": f.peerAddress, "name": f.name} {
		if v != "" {
			attrs[attr] = v
		}
	}
	return attrs, nil
}

// ipsecSiteConnectionAttrs adds the name→ID references to the scalars.
func ipsecSiteConnectionAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *ipsecSiteConnectionFlags) (map[string]any, error) {
	attrs, err := ipsecSiteConnectionScalars(f)
	if err != nil {
		return nil, err
	}
	refs := []struct {
		attr, ref string
		resolver  func(context.Context, *gophercloud.ServiceClient, string) (string, error)
	}{
		{"local_ep_group_id", f.localEndpointGroup, resolveVPNEndpointGroupID},
		{"peer_ep_group_id", f.peerEndpointGroup, resolveVPNEndpointGroupID},
		{"vpnservice_id", f.vpnService, resolveVPNServiceID},
		{"ikepolicy_id", f.ikePolicy, resolveIKEPolicyID},
		{"ipsecpolicy_id", f.ipsecPolicy, resolveIPsecPolicyID},
	}
	for _, r := range refs {
		if r.ref == "" {
			continue
		}
		id, err := r.resolver(ctx, client, r.ref)
		if err != nil {
			return nil, err
		}
		attrs[r.attr] = id
	}
	return attrs, nil
}

func newIPsecSiteConnectionCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := newIPsecSiteConnectionFlags()
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an IPsec site connection",
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
			f.name = args[0]
			return runIPsecSiteConnectionCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	bindIPsecSiteConnectionCommon(cmd, f)
	fl := cmd.Flags()
	fl.StringVar(&f.psk, "psk", "", "pre-shared key string (required)")
	fl.StringVar(&f.vpnService, "vpnservice", "", "VPN service of the connection (name or ID; required)")
	fl.StringVar(&f.ikePolicy, "ikepolicy", "", "IKE policy of the connection (name or ID; required)")
	fl.StringVar(&f.ipsecPolicy, "ipsecpolicy", "", "IPsec policy of the connection (name or ID; required)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	for _, name := range []string{"peer-id", "peer-address", "psk", "vpnservice", "ikepolicy", "ipsecpolicy"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// runIPsecSiteConnectionCreate mirrors upstream CreateIPsecSiteConnection,
// including its two cross-flag checks: both endpoint groups or neither, and
// either endpoint groups or peer CIDRs.
func runIPsecSiteConnectionCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *ipsecSiteConnectionFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	if (f.localEndpointGroup == "") != (f.peerEndpointGroup == "") {
		return errors.New("you must specify both --local-endpoint-group and --peer-endpoint-group")
	}
	if len(f.peerCIDRs) == 0 && f.localEndpointGroup == "" {
		return errors.New("you must specify endpoint groups or --peer-cidr")
	}
	attrs, err := ipsecSiteConnectionAttrs(ctx, client, f)
	if err != nil {
		return err
	}
	attrs["psk"] = f.psk
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	s, err := vpnExtracted(siteconnections.Create(ctx, client, vpnBody{key: "ipsec_site_connection", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("creating IPsec site connection: %w", err)
	}
	fields, values := ipsecSiteConnectionShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func newIPsecSiteConnectionSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := newIPsecSiteConnectionFlags()
	cmd := &cobra.Command{
		Use:   "set <ipsec-site-connection>",
		Short: "Set IPsec site connection properties",
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
			return runIPsecSiteConnectionSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindIPsecSiteConnectionCommon(cmd, f)
	cmd.Flags().StringVar(&f.name, "name", "", "new name for the connection")
	return cmd
}

func runIPsecSiteConnectionSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *ipsecSiteConnectionFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := ipsecSiteConnectionAttrs(ctx, client, f)
	if err != nil {
		return err
	}
	if len(attrs) == 0 {
		return errors.New("vpn ipsec site connection set requires at least one attribute flag")
	}
	id, err := resolveIPsecSiteConnectionID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := vpnExtracted(siteconnections.Update(ctx, client, id, vpnBody{key: "ipsec_site_connection", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("updating IPsec site connection %s: %w", ref, err)
	}
	fields, values := ipsecSiteConnectionShowFields(s)
	return o.WriteSingle(w, fields, values)
}
