package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "bgpvpn network association" and "bgpvpn router association", plus the verb
// scaffolding the port associations (bgpvpn_port_assoc.go) share with them.
// Every association verb names its BGP VPN positionally; show, set, unset and
// delete take the association ID first and the BGP VPN last, as upstream does.

// bgpvpnAssocKind describes one association resource for the shared verbs.
type bgpvpnAssocKind struct {
	resource string // "network", "router", "port"
	// explain annotates an error from the association's endpoints.
	explain func(context.Context, *gophercloud.ServiceClient, error) error
}

func (k bgpvpnAssocKind) label() string { return k.resource + " association" }

func (k bgpvpnAssocKind) idArg() string { return "<" + k.resource + "-association-id>" }

var (
	bgpvpnNetworkAssoc = bgpvpnAssocKind{resource: "network", explain: explainBGPVPN}
	bgpvpnRouterAssoc  = bgpvpnAssocKind{resource: "router", explain: explainBGPVPN}
	bgpvpnPortAssoc    = bgpvpnAssocKind{resource: "port", explain: explainBGPVPNRoutesCtl}
)

// explainBGPVPN is explainMissingService for the bgpvpn extension, in the
// shape bgpvpnAssocKind.explain takes.
func explainBGPVPN(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	return explainMissingService(ctx, client, err, extBGPVPN)
}

func newBGPVPNAssocParent(kind bgpvpnAssocKind, verbs ...*cobra.Command) *cobra.Command {
	assoc := &cobra.Command{Use: "association", Short: "Manage BGP VPN " + kind.resource + " associations"}
	assoc.AddCommand(verbs...)
	parent := &cobra.Command{Use: kind.resource, Short: "BGP VPN " + kind.resource + " association commands"}
	parent.AddCommand(assoc)
	return parent
}

// bgpvpnAssocRef names an association's two ends as given on the command
// line: the BGP VPN, and the resource to associate (create) or the association
// ID (set/unset).
type bgpvpnAssocRef struct {
	bgpvpn string
	target string
}

// bgpvpnAssocCreateFlags carries the create verbs' --project pair.
type bgpvpnAssocCreateFlags struct {
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func bindBGPVPNAssocProject(cmd *cobra.Command, f *bgpvpnAssocCreateFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
}

// newBGPVPNAssocCreateCommand builds a "create <bgpvpn> <resource>" verb whose
// RunE resolves --project and hands over to run.
func newBGPVPNAssocCreateCommand(a *auth.Options, o *output.Options, kind bgpvpnAssocKind, f *bgpvpnAssocCreateFlags,
	run func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnRef, resourceRef string, flags flagSet, w io.Writer) error,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create " + bgpvpnRefArg + " <" + kind.resource + ">",
		Short: "Create a BGP VPN " + kind.label(),
		Args:  cobra.ExactArgs(2),
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
			return run(ctx, client, args[0], args[1], cmd.Flags(), cmd.OutOrStdout())
		},
	}
	bindBGPVPNAssocProject(cmd, f)
	return cmd
}

func newBGPVPNAssocShowCommand(a *auth.Options, o *output.Options, kind bgpvpnAssocKind,
	run func(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, bgpvpnRef, id string, w io.Writer) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   "show " + kind.idArg() + " " + bgpvpnRefArg,
		Short: "Show a BGP VPN " + kind.label(),
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
			return run(ctx, client, o, args[1], args[0], cmd.OutOrStdout())
		},
	}
}

type bgpvpnAssocListFlags struct {
	long       bool
	properties []string
}

func newBGPVPNAssocListCommand(a *auth.Options, o *output.Options, kind bgpvpnAssocKind,
	run func(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, bgpvpnRef string, f *bgpvpnAssocListFlags, w io.Writer) error,
) *cobra.Command {
	f := &bgpvpnAssocListFlags{}
	cmd := &cobra.Command{
		Use:   "list " + bgpvpnRefArg,
		Short: "List a BGP VPN's " + kind.resource + " associations",
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
			return run(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, flagBGPVPNLong, false, bgpvpnLongHelp)
	fl.StringArrayVar(&f.properties, flagBGPVPNProperty, nil, bgpvpnPropertyHelp)
	return cmd
}

// bgpvpnAssocDeleter deletes one association of a BGP VPN.
type bgpvpnAssocDeleter func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnID, id string) error

func newBGPVPNAssocDeleteCommand(a *auth.Options, kind bgpvpnAssocKind, del bgpvpnAssocDeleter) *cobra.Command {
	return &cobra.Command{
		Use:   "delete " + kind.idArg() + " [" + kind.idArg() + " ...] " + bgpvpnRefArg,
		Short: "Delete BGP VPN " + kind.resource + " association(s)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPVPNAssocDelete(ctx, client, kind, del, args[len(args)-1], args[:len(args)-1])
		},
	}
}

// runBGPVPNAssocDelete resolves the BGP VPN once and deletes every association
// ID, attempting all of them (upstream's "Failed to delete N of M").
func runBGPVPNAssocDelete(ctx context.Context, client *gophercloud.ServiceClient, kind bgpvpnAssocKind,
	del bgpvpnAssocDeleter, bgpvpnRef string, ids []string,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, bgpvpnRef)
	if err != nil {
		return err
	}
	return batchdelete.Each(ids, func(id string) error {
		if err := del(ctx, client, bgpvpnID, id); err != nil {
			return kind.explain(ctx, client, fmt.Errorf("deleting %s %s: %w", kind.label(), id, err))
		}
		return nil
	})
}

// assocListQuery builds an association list's query from --property.
func assocListQuery(f *bgpvpnAssocListFlags) (bgpvpnQuery, error) {
	return parseBGPVPNProperties(f.properties)
}

// --- network association -----------------------------------------------------

func newBGPVPNNetworkAssocCommand(a *auth.Options, o *output.Options) *cobra.Command {
	kind := bgpvpnNetworkAssoc
	cf := &bgpvpnAssocCreateFlags{}
	create := newBGPVPNAssocCreateCommand(a, o, kind, cf,
		func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnRef, networkRef string, _ flagSet, w io.Writer) error {
			return runBGPVPNNetworkAssocCreate(ctx, client, o, bgpvpnAssocRef{bgpvpnRef, networkRef}, cf.projectID, w)
		})
	return newBGPVPNAssocParent(kind,
		create,
		newBGPVPNAssocDeleteCommand(a, kind, func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnID, id string) error {
			return bgpvpns.DeleteNetworkAssociation(ctx, client, bgpvpnID, id).ExtractErr()
		}),
		newBGPVPNAssocListCommand(a, o, kind, runBGPVPNNetworkAssocList),
		newBGPVPNAssocShowCommand(a, o, kind, runBGPVPNNetworkAssocShow),
	)
}

func writeBGPVPNNetworkAssoc(o *output.Options, w io.Writer, n *bgpvpns.NetworkAssociation) error {
	return o.WriteSingle(w, []string{"id", "network_id", "project_id"}, []any{n.ID, n.NetworkID, n.ProjectID})
}

func runBGPVPNNetworkAssocCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref bgpvpnAssocRef, projectID string, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, ref.bgpvpn)
	if err != nil {
		return err
	}
	networkID, err := resolveNetworkID(ctx, client, ref.target)
	if err != nil {
		return err
	}
	n, err := bgpvpns.CreateNetworkAssociation(ctx, client, bgpvpnID, bgpvpns.CreateNetworkAssociationOpts{
		NetworkID: networkID,
		ProjectID: projectID,
	}).Extract()
	if err != nil {
		return explainBGPVPN(ctx, client, fmt.Errorf("creating network association: %w", err))
	}
	return writeBGPVPNNetworkAssoc(o, w, n)
}

func runBGPVPNNetworkAssocShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	bgpvpnRef, id string, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, bgpvpnRef)
	if err != nil {
		return err
	}
	n, err := bgpvpns.GetNetworkAssociation(ctx, client, bgpvpnID, id).Extract()
	if err != nil {
		return explainBGPVPN(ctx, client, fmt.Errorf("showing network association %s: %w", id, err))
	}
	return writeBGPVPNNetworkAssoc(o, w, n)
}

func runBGPVPNNetworkAssocList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
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
	pages, err := bgpvpns.ListNetworkAssociations(client, bgpvpnID, q).AllPages(ctx)
	if err != nil {
		return explainBGPVPN(ctx, client, fmt.Errorf("listing network associations: %w", err))
	}
	all, err := bgpvpns.ExtractNetworkAssociations(pages)
	if err != nil {
		return fmt.Errorf("parsing the network association list: %w", err)
	}
	cols := []string{"ID", "Network ID"}
	if f.long {
		cols = []string{"ID", "Project", "Network ID"}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, n := range all {
		if f.long {
			t.Rows = append(t.Rows, []any{n.ID, n.ProjectID, n.NetworkID})
		} else {
			t.Rows = append(t.Rows, []any{n.ID, n.NetworkID})
		}
	}
	return o.WriteList(w, t)
}

// --- router association ------------------------------------------------------

// Upstream spells this pair with underscores (--advertise_extra_routes,
// --no-advertise_extra_routes); koc registers the same names.
const (
	flagAdvertiseExtraRoutes   = "advertise_extra_routes"
	flagNoAdvertiseExtraRoutes = "no-advertise_extra_routes"
)

// bgpvpnAdvertiseFlags is a mutually exclusive advertise/no-advertise pair.
type bgpvpnAdvertiseFlags struct {
	advertise   bool
	noAdvertise bool
}

// value is upstream's _args2body for the pair: on create and set the flag
// means what it says, on unset it is inverted.
func (f bgpvpnAdvertiseFlags) value(unset bool) *bool {
	switch {
	case f.advertise:
		return boolPtr(!unset)
	case f.noAdvertise:
		return boolPtr(unset)
	}
	return nil
}

func bindAdvertiseExtraRoutes(cmd *cobra.Command, f *bgpvpnAdvertiseFlags, verb string) {
	on, off := "advertise the router's routes to the BGP VPN", "do not advertise the router's routes to the BGP VPN"
	if verb == "unset" {
		on, off = off, on
	}
	cmd.Flags().BoolVar(&f.advertise, flagAdvertiseExtraRoutes, false, on)
	cmd.Flags().BoolVar(&f.noAdvertise, flagNoAdvertiseExtraRoutes, false, off)
	cmd.MarkFlagsMutuallyExclusive(flagAdvertiseExtraRoutes, flagNoAdvertiseExtraRoutes)
}

// routerAdvertiseExtraRoutes mirrors upstream exactly: the attribute is always
// sent and defaults to false — even on create, whose help calls advertising the
// default, and even on a set/unset given neither flag.
func routerAdvertiseExtraRoutes(f bgpvpnAdvertiseFlags, unset bool) *bool {
	if v := f.value(unset); v != nil {
		return v
	}
	return boolPtr(false)
}

func newBGPVPNRouterAssocCommand(a *auth.Options, o *output.Options) *cobra.Command {
	kind := bgpvpnRouterAssoc
	cf := &bgpvpnAssocCreateFlags{}
	adv := &bgpvpnAdvertiseFlags{}
	create := newBGPVPNAssocCreateCommand(a, o, kind, cf,
		func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnRef, routerRef string, _ flagSet, w io.Writer) error {
			return runBGPVPNRouterAssocCreate(ctx, client, o, bgpvpnAssocRef{bgpvpnRef, routerRef}, cf.projectID, *adv, w)
		})
	bindAdvertiseExtraRoutes(create, adv, "create")
	return newBGPVPNAssocParent(kind,
		create,
		newBGPVPNAssocDeleteCommand(a, kind, func(ctx context.Context, client *gophercloud.ServiceClient, bgpvpnID, id string) error {
			return bgpvpns.DeleteRouterAssociation(ctx, client, bgpvpnID, id).ExtractErr()
		}),
		newBGPVPNAssocListCommand(a, o, kind, runBGPVPNRouterAssocList),
		newBGPVPNRouterAssocSetCommand(a, o, false),
		newBGPVPNAssocShowCommand(a, o, kind, runBGPVPNRouterAssocShow),
		newBGPVPNRouterAssocSetCommand(a, o, true),
	)
}

func writeBGPVPNRouterAssoc(o *output.Options, w io.Writer, r *bgpvpns.RouterAssociation) error {
	return o.WriteSingle(w,
		[]string{"advertise_extra_routes", "id", "project_id", "router_id"},
		[]any{r.AdvertiseExtraRoutes, r.ID, r.ProjectID, r.RouterID})
}

func runBGPVPNRouterAssocCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref bgpvpnAssocRef, projectID string, adv bgpvpnAdvertiseFlags, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, ref.bgpvpn)
	if err != nil {
		return err
	}
	routerID, err := resolveRouterID(ctx, client, ref.target)
	if err != nil {
		return err
	}
	r, err := bgpvpns.CreateRouterAssociation(ctx, client, bgpvpnID, bgpvpns.CreateRouterAssociationOpts{
		RouterID:             routerID,
		ProjectID:            projectID,
		AdvertiseExtraRoutes: routerAdvertiseExtraRoutes(adv, false),
	}).Extract()
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("creating router association: %w", err))
	}
	return writeBGPVPNRouterAssoc(o, w, r)
}

func newBGPVPNRouterAssocSetCommand(a *auth.Options, o *output.Options, unset bool) *cobra.Command {
	adv := &bgpvpnAdvertiseFlags{}
	verb, short := "set", "Set BGP VPN router association properties"
	if unset {
		verb, short = "unset", "Unset BGP VPN router association properties"
	}
	cmd := &cobra.Command{
		Use:   verb + " " + bgpvpnRouterAssoc.idArg() + " " + bgpvpnRefArg,
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
			return runBGPVPNRouterAssocUpdate(ctx, client, o, bgpvpnAssocRef{args[1], args[0]}, *adv, unset, cmd.OutOrStdout())
		},
	}
	bindAdvertiseExtraRoutes(cmd, adv, verb)
	return cmd
}

func runBGPVPNRouterAssocUpdate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref bgpvpnAssocRef, adv bgpvpnAdvertiseFlags, unset bool, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, ref.bgpvpn)
	if err != nil {
		return err
	}
	r, err := bgpvpns.UpdateRouterAssociation(ctx, client, bgpvpnID, ref.target, bgpvpns.UpdateRouterAssociationOpts{
		AdvertiseExtraRoutes: routerAdvertiseExtraRoutes(adv, unset),
	}).Extract()
	if err != nil {
		return explainBGPVPNRoutesCtl(ctx, client, fmt.Errorf("updating router association %s: %w", ref.target, err))
	}
	return writeBGPVPNRouterAssoc(o, w, r)
}

func runBGPVPNRouterAssocShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	bgpvpnRef, id string, w io.Writer,
) error {
	bgpvpnID, err := resolveBGPVPNID(ctx, client, bgpvpnRef)
	if err != nil {
		return err
	}
	r, err := bgpvpns.GetRouterAssociation(ctx, client, bgpvpnID, id).Extract()
	if err != nil {
		return explainBGPVPN(ctx, client, fmt.Errorf("showing router association %s: %w", id, err))
	}
	return writeBGPVPNRouterAssoc(o, w, r)
}

func runBGPVPNRouterAssocList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
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
	pages, err := bgpvpns.ListRouterAssociations(client, bgpvpnID, q).AllPages(ctx)
	if err != nil {
		return explainBGPVPN(ctx, client, fmt.Errorf("listing router associations: %w", err))
	}
	all, err := bgpvpns.ExtractRouterAssociations(pages)
	if err != nil {
		return fmt.Errorf("parsing the router association list: %w", err)
	}
	cols := []string{"ID", "Router ID"}
	if f.long {
		cols = []string{"ID", "Project", "Router ID", "Advertise extra routes"}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		if f.long {
			t.Rows = append(t.Rows, []any{r.ID, r.ProjectID, r.RouterID, r.AdvertiseExtraRoutes})
		} else {
			t.Rows = append(t.Rows, []any{r.ID, r.RouterID})
		}
	}
	return o.WriteList(w, t)
}
