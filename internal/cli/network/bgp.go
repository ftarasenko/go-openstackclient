package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/speakers"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// BGP dynamic routing (neutron-dynamic-routing) mirrors upstream's
// [openstack.network.v2.dynamic_routing] namespace
// (network/v2/dynamic_routing/{bgp_speaker,bgp_peer,bgp_dragent}.py): "bgp
// speaker", "bgp peer" and "bgp dragent" are nested parents so the upstream
// words resolve unambiguously ("bgp speaker list advertised routes").

// Flag names of the dynamic-routing verbs; batch-specific so they cannot
// collide with the shared constants in flagnames.go.
const (
	bgpFlagName                     = "name"
	bgpFlagLocalAS                  = "local-as"
	bgpFlagIPVersion                = "ip-version"
	bgpFlagAdvertiseFIPHostRoutes   = "advertise-floating-ip-host-routes"
	bgpFlagNoAdvertiseFIPHostRoutes = "no-advertise-floating-ip-host-routes"
	bgpFlagAdvertiseTenantNetworks  = "advertise-tenant-networks"
	bgpFlagNoAdvertiseTenantNets    = "no-advertise-tenant-networks"
	bgpFlagPassword                 = "password"
)

// bgpMinAS and bgpMaxAS bound a 4-byte AS number (upstream bgp_peer.MIN_AS_NUM
// / MAX_AS_NUM, and neutron-dynamic-routing's own validator).
const (
	bgpMinAS = 1
	bgpMaxAS = 4294967295
)

// newBGPCommands builds the "bgp" top-level noun.
func newBGPCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	bgp := &cobra.Command{
		Use: "bgp",
		Short: "Manage BGP dynamic routing (requires neutron-dynamic-routing: the bgp extension; " +
			"the dragent verbs need bgp_dragent_scheduler)",
	}
	bgp.AddCommand(newBGPSpeakerCommand(a, o))
	bgp.AddCommand(newBGPPeerCommand(a, o))
	bgp.AddCommand(newBGPDRAgentCommand(a, o))
	return []*cobra.Command{bgp}
}

// bgptapBody is a whole request body for the dynamic-routing and TaaS writes
// whose gophercloud opts cannot say what upstream sends: speakers.CreateOpts
// and UpdateOpts always send both advertise_* booleans (upstream sends only
// the ones given) and lack project_id; peers.CreateOpts lacks project_id and
// UpdateOpts cannot send an empty name; tapmirrors.CreateOpts always sends
// name, lacks project_id and types directions as integers. None of those opts
// carries a RevisionNumber, so there is no If-Match guard to preserve. One
// value satisfies every builder interface it is handed to.
type bgptapBody struct {
	key   string
	attrs map[string]any
}

func (b bgptapBody) body() (map[string]any, error) {
	attrs := b.attrs
	if attrs == nil {
		attrs = map[string]any{}
	}
	return map[string]any{b.key: attrs}, nil
}

func (b bgptapBody) ToSpeakerCreateMap() (map[string]any, error)   { return b.body() }
func (b bgptapBody) ToSpeakerUpdateMap() (map[string]any, error)   { return b.body() }
func (b bgptapBody) ToPeerCreateMap() (map[string]any, error)      { return b.body() }
func (b bgptapBody) ToPeerUpdateMap() (map[string]any, error)      { return b.body() }
func (b bgptapBody) ToTapMirrorCreateMap() (map[string]any, error) { return b.body() }

// parseASNumber validates an AS number the way neutron-dynamic-routing does
// (an integer in [1, 4294967295]) so a typo fails before the request. Upstream
// sends the string it was given and neutron converts it; the integer is the
// same value on the wire after that conversion.
func parseASNumber(flag, s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < bgpMinAS || n > bgpMaxAS {
		return 0, fmt.Errorf("--%s must be an integer in [%d, %d], got %q", flag, bgpMinAS, bgpMaxAS, s)
	}
	return n, nil
}

// resolveBGPSpeakerID resolves a BGP speaker name or ID. speakers.List takes
// no filter, so the name query is added here; the exact-name match guards a
// server that ignores it.
func resolveBGPSpeakerID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	id, err := resolveByName(client, "BGP speaker", nameOrID, func(c *gophercloud.ServiceClient) ([]speakers.BGPSpeaker, error) {
		u := c.ServiceURL("bgp-speakers") + "?" + url.Values{"name": {nameOrID}}.Encode()
		pages, err := pagination.NewPager(c, u, func(r pagination.PageResult) pagination.Page {
			return speakers.BGPSpeakerPage{SinglePageBase: pagination.SinglePageBase(r)}
		}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := speakers.ExtractBGPSpeakers(pages)
		return keepUnmatched(all, func(s speakers.BGPSpeaker) bool { return s.Name != nameOrID }), err
	}, func(s speakers.BGPSpeaker) string { return s.ID })
	return id, explainMissingService(ctx, client, err, extBGP)
}

func newBGPSpeakerCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "speaker",
		Short: "Manage BGP speakers (requires the bgp extension)",
	}
	cmd.AddCommand(newBGPSpeakerCreateCommand(a, o))
	cmd.AddCommand(newBGPSpeakerDeleteCommand(a, o))
	cmd.AddCommand(newBGPSpeakerListCommand(a, o))
	cmd.AddCommand(newBGPSpeakerSetCommand(a, o))
	cmd.AddCommand(newBGPSpeakerShowCommand(a, o))
	cmd.AddCommand(newBGPSpeakerAddCommand(a, o))
	cmd.AddCommand(newBGPSpeakerRemoveCommand(a, o))
	return cmd
}

func bgpSpeakerShowFields(s *speakers.BGPSpeaker) ([]string, []any) {
	fields := []string{
		"id", "name", "local_as", "ip_version",
		"advertise_floating_ip_host_routes", "advertise_tenant_networks",
		"networks", "peers", "project_id",
	}
	values := []any{
		s.ID, s.Name, s.LocalAS, s.IPVersion,
		s.AdvertiseFloatingIPHostRoutes, s.AdvertiseTenantNetworks,
		s.Networks, s.Peers, s.ProjectID,
	}
	return fields, values
}

// bgpSpeakerAdvertiseFlags carries the two advertise on/off pairs shared by
// create and set (upstream add_common_arguments).
type bgpSpeakerAdvertiseFlags struct {
	advertiseFIP      bool
	noAdvertiseFIP    bool
	advertiseTenant   bool
	noAdvertiseTenant bool
}

func bindBGPSpeakerAdvertiseFlags(cmd *cobra.Command, f *bgpSpeakerAdvertiseFlags) {
	fl := cmd.Flags()
	fl.BoolVar(&f.advertiseFIP, bgpFlagAdvertiseFIPHostRoutes, false, "advertise floating IP host routes (neutron's default)")
	fl.BoolVar(&f.noAdvertiseFIP, bgpFlagNoAdvertiseFIPHostRoutes, false, "do not advertise floating IP host routes")
	fl.BoolVar(&f.advertiseTenant, bgpFlagAdvertiseTenantNetworks, false, "advertise tenant network routes (neutron's default)")
	fl.BoolVar(&f.noAdvertiseTenant, bgpFlagNoAdvertiseTenantNets, false, "do not advertise tenant network routes")
	cmd.MarkFlagsMutuallyExclusive(bgpFlagAdvertiseFIPHostRoutes, bgpFlagNoAdvertiseFIPHostRoutes)
	cmd.MarkFlagsMutuallyExclusive(bgpFlagAdvertiseTenantNetworks, bgpFlagNoAdvertiseTenantNets)
}

// apply adds the advertise_* attributes that were given — only those, as
// upstream's _get_attrs does.
func (f *bgpSpeakerAdvertiseFlags) apply(attrs map[string]any) {
	switch {
	case f.advertiseTenant:
		attrs["advertise_tenant_networks"] = true
	case f.noAdvertiseTenant:
		attrs["advertise_tenant_networks"] = false
	}
	switch {
	case f.advertiseFIP:
		attrs["advertise_floating_ip_host_routes"] = true
	case f.noAdvertiseFIP:
		attrs["advertise_floating_ip_host_routes"] = false
	}
}

type bgpSpeakerCreateFlags struct {
	localAS       string
	ipVersion     int
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
	bgpSpeakerAdvertiseFlags
}

func newBGPSpeakerCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &bgpSpeakerCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a BGP speaker",
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
			return runBGPSpeakerCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.localAS, bgpFlagLocalAS, "", fmt.Sprintf("local AS number (integer in [%d, %d])", bgpMinAS, bgpMaxAS))
	fl.IntVar(&f.ipVersion, bgpFlagIPVersion, 4, "IP version of the BGP speaker (4 or 6)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindBGPSpeakerAdvertiseFlags(cmd, &f.bgpSpeakerAdvertiseFlags)
	_ = cmd.MarkFlagRequired(bgpFlagLocalAS)
	return cmd
}

func runBGPSpeakerCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *bgpSpeakerCreateFlags, w io.Writer) error {
	localAS, err := parseASNumber(bgpFlagLocalAS, f.localAS)
	if err != nil {
		return err
	}
	if f.ipVersion != 4 && f.ipVersion != 6 {
		return fmt.Errorf("--%s must be 4 or 6, got %d", bgpFlagIPVersion, f.ipVersion)
	}
	attrs := map[string]any{"name": name, "local_as": localAS, "ip_version": f.ipVersion}
	f.apply(attrs)
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	s, err := speakers.Create(ctx, client, bgptapBody{key: "bgp_speaker", attrs: attrs}).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("creating BGP speaker: %w", err), extBGP)
	}
	fields, values := bgpSpeakerShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func newBGPSpeakerDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <bgp-speaker> [<bgp-speaker> ...]",
		Short: "Delete BGP speaker(s)",
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
			return runBGPSpeakerDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runBGPSpeakerDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveBGPSpeakerID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := speakers.Delete(ctx, client, id).ExtractErr(); err != nil {
			return explainMissingService(ctx, client, fmt.Errorf("deleting BGP speaker %s: %w", ref, err), extBGP)
		}
		_, err = fmt.Fprintf(w, "Deleted BGP speaker %s\n", ref)
		return err
	})
}

func newBGPSpeakerListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var agentID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List BGP speakers",
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
			return runBGPSpeakerList(ctx, client, o, agentID, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "list the BGP speakers hosted by this dynamic routing agent (ID only)")
	cmd.AddCommand(newBGPSpeakerListAdvertisedCommand(a, o))
	return cmd
}

// runBGPSpeakerList lists every speaker, or with agentID the ones that
// dynamic routing agent hosts (GET /agents/{id}/bgp-drinstances).
func runBGPSpeakerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, agentID string, w io.Writer) error {
	var (
		all []speakers.BGPSpeaker
		err error
	)
	if agentID != "" {
		var pages pagination.Page
		if pages, err = agents.ListBGPSpeakers(client, agentID).AllPages(ctx); err != nil {
			return explainMissingService(ctx, client,
				fmt.Errorf("listing BGP speakers hosted by agent %s: %w", agentID, err), extBGPDRAgentSchedule)
		}
		all, err = agents.ExtractBGPSpeakers(pages)
	} else {
		var pages pagination.Page
		if pages, err = speakers.List(client).AllPages(ctx); err != nil {
			return explainMissingService(ctx, client, fmt.Errorf("listing BGP speakers: %w", err), extBGP)
		}
		all, err = speakers.ExtractBGPSpeakers(pages)
	}
	if err != nil {
		return fmt.Errorf("parsing BGP speaker list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Local AS", "IP Version"}, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		t.Rows = append(t.Rows, []any{s.ID, s.Name, s.LocalAS, s.IPVersion})
	}
	return o.WriteList(w, t)
}

type bgpSpeakerSetFlags struct {
	name string
	bgpSpeakerAdvertiseFlags
}

func newBGPSpeakerSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &bgpSpeakerSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <bgp-speaker>",
		Short: "Set BGP speaker properties",
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
			return runBGPSpeakerSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.name, bgpFlagName, "", "new name for the BGP speaker")
	bindBGPSpeakerAdvertiseFlags(cmd, &f.bgpSpeakerAdvertiseFlags)
	return cmd
}

func runBGPSpeakerSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *bgpSpeakerSetFlags, flags flagSet, w io.Writer) error {
	attrs := map[string]any{}
	if flags.Changed(bgpFlagName) {
		attrs["name"] = f.name
	}
	f.apply(attrs)
	if len(attrs) == 0 {
		return fmt.Errorf("bgp speaker set requires at least one attribute flag")
	}
	id, err := resolveBGPSpeakerID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := speakers.Update(ctx, client, id, bgptapBody{key: "bgp_speaker", attrs: attrs}).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("updating BGP speaker %s: %w", ref, err), extBGP)
	}
	fields, values := bgpSpeakerShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func newBGPSpeakerShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <bgp-speaker>",
		Short: "Show details of a BGP speaker",
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
			return runBGPSpeakerShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runBGPSpeakerShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveBGPSpeakerID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := speakers.Get(ctx, client, id).Extract()
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("getting BGP speaker %s: %w", ref, err), extBGP)
	}
	fields, values := bgpSpeakerShowFields(s)
	return o.WriteSingle(w, fields, values)
}

// "bgp speaker add network|peer" and "bgp speaker remove network|peer".

func newBGPSpeakerAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "add", Short: "Add a network or peer to a BGP speaker"}
	cmd.AddCommand(bgpSpeakerTargetCommand(a, o, "network", "<network>", "Add a network to a BGP speaker", runBGPSpeakerAddNetwork))
	cmd.AddCommand(bgpSpeakerTargetCommand(a, o, "peer", "<bgp-peer>", "Add a peer to a BGP speaker", runBGPSpeakerAddPeer))
	return cmd
}

func newBGPSpeakerRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "remove", Short: "Remove a network or peer from a BGP speaker"}
	cmd.AddCommand(bgpSpeakerTargetCommand(a, o, "network", "<network>", "Remove a network from a BGP speaker", runBGPSpeakerRemoveNetwork))
	cmd.AddCommand(bgpSpeakerTargetCommand(a, o, "peer", "<bgp-peer>", "Remove a peer from a BGP speaker", runBGPSpeakerRemovePeer))
	return cmd
}

// bgpSpeakerTargetCommand builds one "<verb> <target> <bgp-speaker> <target>"
// leaf; the four differ only in the seam they call.
func bgpSpeakerTargetCommand(a *auth.Options, o *output.Options, use, metavar, short string,
	run func(context.Context, *gophercloud.ServiceClient, string, string, io.Writer) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <bgp-speaker> " + metavar,
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
			return run(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runBGPSpeakerAddNetwork(ctx context.Context, client *gophercloud.ServiceClient, speakerRef, netRef string, w io.Writer) error {
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	netID, err := resolveNetworkID(ctx, client, netRef)
	if err != nil {
		return err
	}
	opts := speakers.AddGatewayNetworkOpts{NetworkID: netID}
	if _, err := speakers.AddGatewayNetwork(ctx, client, speakerID, opts).Extract(); err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("adding network %s to BGP speaker %s: %w", netRef, speakerRef, err), extBGP)
	}
	_, err = fmt.Fprintf(w, "Added network %s to BGP speaker %s\n", netRef, speakerRef)
	return err
}

func runBGPSpeakerRemoveNetwork(ctx context.Context, client *gophercloud.ServiceClient, speakerRef, netRef string, w io.Writer) error {
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	netID, err := resolveNetworkID(ctx, client, netRef)
	if err != nil {
		return err
	}
	opts := speakers.RemoveGatewayNetworkOpts{NetworkID: netID}
	if err := speakers.RemoveGatewayNetwork(ctx, client, speakerID, opts).ExtractErr(); err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("removing network %s from BGP speaker %s: %w", netRef, speakerRef, err), extBGP)
	}
	_, err = fmt.Fprintf(w, "Removed network %s from BGP speaker %s\n", netRef, speakerRef)
	return err
}

func runBGPSpeakerAddPeer(ctx context.Context, client *gophercloud.ServiceClient, speakerRef, peerRef string, w io.Writer) error {
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	peerID, err := resolveBGPPeerID(ctx, client, peerRef)
	if err != nil {
		return err
	}
	opts := speakers.AddBGPPeerOpts{BGPPeerID: peerID}
	if _, err := speakers.AddBGPPeer(ctx, client, speakerID, opts).Extract(); err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("adding BGP peer %s to BGP speaker %s: %w", peerRef, speakerRef, err), extBGP)
	}
	_, err = fmt.Fprintf(w, "Added BGP peer %s to BGP speaker %s\n", peerRef, speakerRef)
	return err
}

func runBGPSpeakerRemovePeer(ctx context.Context, client *gophercloud.ServiceClient, speakerRef, peerRef string, w io.Writer) error {
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	peerID, err := resolveBGPPeerID(ctx, client, peerRef)
	if err != nil {
		return err
	}
	opts := speakers.RemoveBGPPeerOpts{BGPPeerID: peerID}
	if err := speakers.RemoveBGPPeer(ctx, client, speakerID, opts).ExtractErr(); err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("removing BGP peer %s from BGP speaker %s: %w", peerRef, speakerRef, err), extBGP)
	}
	_, err = fmt.Fprintf(w, "Removed BGP peer %s from BGP speaker %s\n", peerRef, speakerRef)
	return err
}

// "bgp speaker list advertised routes": the leaf is "routes" under the
// "list advertised" parents, matching upstream's four words.

func newBGPSpeakerListAdvertisedCommand(a *auth.Options, o *output.Options) *cobra.Command {
	advertised := &cobra.Command{Use: "advertised", Short: "List what a BGP speaker advertises"}
	advertised.AddCommand(&cobra.Command{
		Use:   "routes <bgp-speaker>",
		Short: "List the routes a BGP speaker advertises",
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
			return runBGPSpeakerListAdvertisedRoutes(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	return advertised
}

func runBGPSpeakerListAdvertisedRoutes(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveBGPSpeakerID(ctx, client, ref)
	if err != nil {
		return err
	}
	pages, err := speakers.GetAdvertisedRoutes(client, id).AllPages(ctx)
	if err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("listing routes advertised by BGP speaker %s: %w", ref, err), extBGP)
	}
	routes, err := speakers.ExtractAdvertisedRoutes(pages)
	if err != nil {
		return fmt.Errorf("parsing advertised routes: %w", err)
	}
	t := output.Table{Columns: []string{"Destination", "Nexthop"}, Rows: make([][]any, 0, len(routes))}
	for _, r := range routes {
		t.Rows = append(t.Rows, []any{r.Destination, r.NextHop})
	}
	return o.WriteList(w, t)
}
