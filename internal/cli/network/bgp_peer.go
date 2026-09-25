package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/peers"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "bgp peer ..." mirrors upstream network/v2/dynamic_routing/bgp_peer.py.

const bgpAuthTypeNone = "none"

// resolveBGPPeerID resolves a BGP peer name or ID; see resolveBGPSpeakerID.
func resolveBGPPeerID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	id, err := resolveByName(client, "BGP peer", nameOrID, func(c *gophercloud.ServiceClient) ([]peers.BGPPeer, error) {
		u := c.ServiceURL("bgp-peers") + "?" + url.Values{"name": {nameOrID}}.Encode()
		pages, err := pagination.NewPager(c, u, func(r pagination.PageResult) pagination.Page {
			return peers.BGPPeerPage{SinglePageBase: pagination.SinglePageBase(r)}
		}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := peers.ExtractBGPPeers(pages)
		return keepUnmatched(all, func(p peers.BGPPeer) bool { return p.Name != nameOrID }), err
	}, func(p peers.BGPPeer) string { return p.ID })
	return id, explainMissingService(ctx, client, err, extBGP)
}

func newBGPPeerCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peer",
		Short: "Manage BGP peers (requires the bgp extension)",
	}
	cmd.AddCommand(newBGPPeerCreateCommand(a, o))
	cmd.AddCommand(newBGPPeerDeleteCommand(a, o))
	cmd.AddCommand(newBGPPeerListCommand(a, o))
	cmd.AddCommand(newBGPPeerSetCommand(a, o))
	cmd.AddCommand(newBGPPeerShowCommand(a, o))
	return cmd
}

// bgpPeerShowFields renders a peer; neutron never returns the password.
func bgpPeerShowFields(p *peers.BGPPeer) ([]string, []any) {
	fields := []string{"id", "name", "peer_ip", "remote_as", "auth_type", "project_id"}
	values := []any{p.ID, p.Name, p.PeerIP, p.RemoteAS, p.AuthType, p.ProjectID}
	return fields, values
}

type bgpPeerCreateFlags struct {
	peerIP        string
	remoteAS      string
	authType      string
	password      string
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newBGPPeerCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &bgpPeerCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a BGP peer",
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
			return runBGPPeerCreate(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.peerIP, "peer-ip", "", "peer IP address")
	fl.StringVar(&f.remoteAS, "remote-as", "", fmt.Sprintf("peer AS number (integer in [%d, %d])", bgpMinAS, bgpMaxAS))
	fl.StringVar(&f.authType, "auth-type", bgpAuthTypeNone, "authentication algorithm (none, md5)")
	fl.StringVar(&f.password, bgpFlagPassword, "", "authentication password (requires --auth-type md5)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	_ = cmd.MarkFlagRequired("peer-ip")
	_ = cmd.MarkFlagRequired("remote-as")
	return cmd
}

// bgpPeerCreateAttrs applies upstream's _get_attrs checks: auth_type is
// case-folded and always sent, and a password goes with md5 and only with it.
// Upstream also sends "password": null when none was given; omitting it is the
// same thing to neutron on create, whose default is null.
func bgpPeerCreateAttrs(name string, f *bgpPeerCreateFlags, flags flagSet) (map[string]any, error) {
	authType := strings.ToLower(f.authType)
	if authType != bgpAuthTypeNone && authType != "md5" {
		return nil, fmt.Errorf("--auth-type must be none or md5, got %q", f.authType)
	}
	hasPassword := flags.Changed(bgpFlagPassword)
	switch {
	case authType != bgpAuthTypeNone && !hasPassword:
		return nil, fmt.Errorf("--auth-type %s requires --password", authType)
	case authType == bgpAuthTypeNone && hasPassword:
		return nil, fmt.Errorf("--password requires --auth-type md5")
	}
	remoteAS, err := parseASNumber("remote-as", f.remoteAS)
	if err != nil {
		return nil, err
	}
	attrs := map[string]any{"name": name, "peer_ip": f.peerIP, "remote_as": remoteAS, "auth_type": authType}
	if hasPassword {
		attrs["password"] = f.password
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	return attrs, nil
}

func runBGPPeerCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *bgpPeerCreateFlags, flags flagSet, w io.Writer) error {
	attrs, err := bgpPeerCreateAttrs(name, f, flags)
	if err != nil {
		return err
	}
	p, err := peers.Create(ctx, client, bgptapBody{key: "bgp_peer", attrs: attrs}).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("creating BGP peer: %w", err), extBGP)
	}
	fields, values := bgpPeerShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newBGPPeerDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <bgp-peer> [<bgp-peer> ...]",
		Short: "Delete BGP peer(s)",
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
			return runBGPPeerDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runBGPPeerDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveBGPPeerID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := peers.Delete(ctx, client, id).ExtractErr(); err != nil {
			return explainMissingService(ctx, client, fmt.Errorf("deleting BGP peer %s: %w", ref, err), extBGP)
		}
		_, err = fmt.Fprintf(w, "Deleted BGP peer %s\n", ref)
		return err
	})
}

func newBGPPeerListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List BGP peers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPPeerList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runBGPPeerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := peers.List(client).AllPages(ctx)
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("listing BGP peers: %w", err), extBGP)
	}
	all, err := peers.ExtractBGPPeers(pages)
	if err != nil {
		return fmt.Errorf("parsing BGP peer list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Peer IP", "Remote AS"}, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.PeerIP, p.RemoteAS})
	}
	return o.WriteList(w, t)
}

type bgpPeerSetFlags struct {
	name     string
	password string
}

func newBGPPeerSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &bgpPeerSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <bgp-peer>",
		Short: "Set BGP peer properties",
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
			return runBGPPeerSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, bgpFlagName, "", "new name for the BGP peer")
	fl.StringVar(&f.password, bgpFlagPassword, "", "new authentication password")
	return cmd
}

// runBGPPeerSet sends only what was given. Upstream's shared _get_attrs puts
// "password": None into every set, which neutron-dynamic-routing applies —
// renaming an md5 peer that way would also clear its password.
func runBGPPeerSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *bgpPeerSetFlags, flags flagSet, w io.Writer) error {
	attrs := map[string]any{}
	if flags.Changed(bgpFlagName) {
		attrs["name"] = f.name
	}
	if flags.Changed(bgpFlagPassword) {
		attrs["password"] = f.password
	}
	if len(attrs) == 0 {
		return fmt.Errorf("bgp peer set requires at least one attribute flag")
	}
	id, err := resolveBGPPeerID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := peers.Update(ctx, client, id, bgptapBody{key: "bgp_peer", attrs: attrs}).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("updating BGP peer %s: %w", ref, err), extBGP)
	}
	fields, values := bgpPeerShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newBGPPeerShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <bgp-peer>",
		Short: "Show details of a BGP peer",
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
			return runBGPPeerShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runBGPPeerShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveBGPPeerID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := peers.Get(ctx, client, id).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("getting BGP peer %s: %w", ref, err), extBGP)
	}
	fields, values := bgpPeerShowFields(p)
	return o.WriteSingle(w, fields, values)
}
