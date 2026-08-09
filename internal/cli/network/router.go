package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
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

func newRouterListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List routers",
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
			return runRouterList(ctx, client, o, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runRouterList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := routers.List(client, routers.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing routers: %w", err)
	}
	all, err := routers.ExtractRouters(pages)
	if err != nil {
		return fmt.Errorf("parsing router list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Status", "State", "Project"}, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		t.Rows = append(t.Rows, []any{r.ID, r.Name, r.Status, adminState(r.AdminStateUp), r.ProjectID})
	}
	return o.WriteList(w, t)
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
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
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
	var errs []error
	for _, nameOrID := range names {
		id, err := resolveRouterID(ctx, client, nameOrID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := routers.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting router %s: %w", nameOrID, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted router %s\n", nameOrID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	fl.BoolVar(&f.enableSNAT, "enable-snat", false, "enable source NAT on the external gateway")
	fl.BoolVar(&f.disableSNAT, "disable-snat", false, "disable source NAT on the external gateway")
	fl.StringVar(&f.qosPolicy, "qos-policy", "", "QoS policy ID to attach to the external gateway")
	fl.StringArrayVar(&f.route, "route", nil,
		"static route as destination=<cidr>,gateway=<ip> (repeatable; appends unless --no-route is also given)")
	fl.BoolVar(&f.noRoute, "no-route", false, "clear the router's static routes (with --route, replaces them instead)")
	cmd.MarkFlagsMutuallyExclusive("enable-snat", "disable-snat")
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
	snat := enableDisable(flags, f.enableSNAT, f.disableSNAT, "enable-snat", "disable-snat")
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
		var route routers.Route
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, v, err := splitKV(part)
			if err != nil {
				return nil, fmt.Errorf("parsing --route %q: %w", spec, err)
			}
			switch k {
			case "destination":
				route.DestinationCIDR = v
			case "gateway", "nexthop":
				route.NextHop = v
			default:
				return nil, fmt.Errorf("parsing --route %q: unknown key %q", spec, k)
			}
		}
		if route.DestinationCIDR == "" || route.NextHop == "" {
			return nil, fmt.Errorf("--route %q requires both destination and gateway", spec)
		}
		out = append(out, route)
	}
	return out, nil
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
