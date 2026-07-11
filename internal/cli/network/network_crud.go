package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/external"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/mtu"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/provider"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
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

// networkExt is a Network decorated with the mtu, external-net and provider
// extension attributes. Each is an anonymous embed so ExtractInto populates
// them all — the standard gophercloud "network with extensions" pattern.
type networkExt struct {
	networks.Network
	external.NetworkExternalExt
	provider.NetworkProviderExt
	MTUExt
}

func networkShowFields(n *networkExt) ([]string, []any) {
	fields := []string{
		"id", "name", "status", "admin_state_up", "shared", "router:external",
		"mtu", "subnets", "provider:network_type", "provider:physical_network",
		"availability_zone_hints", "description", "project_id", "tags",
		"created_at", "updated_at",
	}
	values := []any{
		n.ID, n.Name, n.Status, n.AdminStateUp, n.Shared, n.External,
		n.MTU, n.Subnets, n.NetworkType, n.PhysicalNetwork,
		n.AvailabilityZoneHints, n.Description, n.ProjectID, n.Tags,
		n.CreatedAt, n.UpdatedAt,
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
			f.externalSet = cmd.Flags().Changed("external")
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runNetworkList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.BoolVar(&f.external, "external", false, "list only external networks (use --external=false for internal)")
	fl.StringVar(&f.name, "name", "", "list networks matching this name")
	return cmd
}

type networkListFlags struct {
	long        bool
	external    bool
	externalSet bool
	name        string
}

func runNetworkList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *networkListFlags, w io.Writer) error {
	base := networks.ListOpts{Name: f.name}
	var opts networks.ListOptsBuilder = base
	if f.externalSet {
		opts = external.ListOptsExt{ListOptsBuilder: base, External: boolPtr(f.external)}
	}
	pages, err := networks.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing networks: %w", err)
	}
	var all []networkExt
	if err := networks.ExtractNetworksInto(pages, &all); err != nil {
		return fmt.Errorf("parsing network list: %w", err)
	}
	return o.WriteList(w, networkListTable(all, f.long))
}

func networkListTable(list []networkExt, long bool) output.Table {
	cols := []string{"ID", "Name", "Subnets"}
	if long {
		cols = []string{"ID", "Name", "Status", "Project", "State", "Shared", "Subnets", "Network Type", "Router:External", "MTU"}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for i := range list {
		n := &list[i]
		if long {
			t.Rows = append(t.Rows, []any{n.ID, n.Name, n.Status, n.ProjectID, adminState(n.AdminStateUp), n.Shared, n.Subnets, n.NetworkType, n.External, n.MTU})
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

type networkCreateFlags struct {
	enable          bool
	disable         bool
	share           bool
	external        bool
	mtu             int
	providerType    string
	providerPhysNet string
	providerSegment string
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
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runNetworkCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the network (admin state up, default)")
	fl.BoolVar(&f.disable, "disable", false, "disable the network (admin state down)")
	fl.BoolVar(&f.share, "share", false, "share the network across projects")
	fl.BoolVar(&f.external, "external", false, "set the network as external")
	fl.IntVar(&f.mtu, "mtu", 0, "maximum transmission unit for the network")
	fl.StringVar(&f.providerType, "provider-network-type", "", "physical network type (flat, vlan, vxlan, ...)")
	fl.StringVar(&f.providerPhysNet, "provider-physical-network", "", "name of the physical network")
	fl.StringVar(&f.providerSegment, "provider-segment", "", "VLAN ID or tunnel ID for the network segment")
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
	m := base["network"].(map[string]any)
	if opts.NetworkType != "" {
		m["provider:network_type"] = opts.NetworkType
	}
	if opts.PhysicalNetwork != "" {
		m["provider:physical_network"] = opts.PhysicalNetwork
	}
	if opts.SegmentationID != "" {
		m["provider:segmentation_id"] = opts.SegmentationID
	}
	return base, nil
}

func runNetworkCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *networkCreateFlags, w io.Writer) error {
	base := networks.CreateOpts{Name: name}
	if f.disable {
		base.AdminStateUp = boolPtr(false)
	} else {
		base.AdminStateUp = boolPtr(true)
	}
	if f.share {
		base.Shared = boolPtr(true)
	}

	var builder networks.CreateOptsBuilder = base
	if f.mtu > 0 {
		builder = mtu.CreateOptsExt{CreateOptsBuilder: builder, MTU: f.mtu}
	}
	if f.external {
		builder = external.CreateOptsExt{CreateOptsBuilder: builder, External: boolPtr(true)}
	}
	if f.providerType != "" || f.providerPhysNet != "" || f.providerSegment != "" {
		builder = providerCreateOpts{
			CreateOptsBuilder: builder,
			NetworkType:       f.providerType,
			PhysicalNetwork:   f.providerPhysNet,
			SegmentationID:    f.providerSegment,
		}
	}

	var n networkExt
	if err := networks.Create(ctx, client, builder).ExtractInto(&n); err != nil {
		return fmt.Errorf("creating network: %w", err)
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
	for _, nameOrID := range names {
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
	}
	return nil
}

type networkSetFlags struct {
	name    string
	mtu     int
	enable  bool
	disable bool
	share   bool
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
	fl.IntVar(&f.mtu, "mtu", 0, "new maximum transmission unit")
	fl.BoolVar(&f.enable, "enable", false, "enable the network (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "disable the network (admin state down)")
	fl.BoolVar(&f.share, "share", false, "share the network across projects")
	return cmd
}

func runNetworkSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *networkSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolveNetworkID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	base := networks.UpdateOpts{}
	changed := false
	if f.name != "" {
		base.Name = &f.name
		changed = true
	}
	if state := enableDisable(flags, f.enable, f.disable); state != nil {
		base.AdminStateUp = state
		changed = true
	}
	if flags.Changed("share") {
		base.Shared = boolPtr(f.share)
		changed = true
	}
	var builder networks.UpdateOptsBuilder = base
	if f.mtu > 0 {
		builder = mtu.UpdateOptsExt{UpdateOptsBuilder: base, MTU: f.mtu}
		changed = true
	}
	if !changed {
		return fmt.Errorf("network set requires at least one attribute flag")
	}
	var n networkExt
	if err := networks.Update(ctx, client, id, builder).ExtractInto(&n); err != nil {
		return fmt.Errorf("updating network %s: %w", nameOrID, err)
	}
	fields, values := networkShowFields(&n)
	return o.WriteSingle(w, fields, values)
}

// flagSet is the small surface of *pflag.FlagSet used by the set/unset seams,
// kept as an interface so tests can drive runNetworkSet without cobra.
type flagSet interface {
	Changed(string) bool
}

func newNetworkUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var share bool
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
			return runNetworkUnset(ctx, client, o, args[0], share, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&share, "share", false, "make the network project-private (unset shared)")
	return cmd
}

func runNetworkUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, share bool, w io.Writer) error {
	id, err := resolveNetworkID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	if !share {
		return fmt.Errorf("network unset requires at least one attribute flag")
	}
	opts := networks.UpdateOpts{Shared: boolPtr(false)}
	var n networkExt
	if err := networks.Update(ctx, client, id, opts).ExtractInto(&n); err != nil {
		return fmt.Errorf("updating network %s: %w", nameOrID, err)
	}
	fields, values := networkShowFields(&n)
	return o.WriteSingle(w, fields, values)
}
