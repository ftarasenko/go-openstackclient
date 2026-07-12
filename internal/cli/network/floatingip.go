package network

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newFloatingIPCommand builds the "ip" child of the "floating" parent, giving
// the two-word OSC command "floating ip ...".
func newFloatingIPCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ip",
		Short: "Manage floating IPs",
	}
	cmd.AddCommand(newFloatingIPListCommand(a, o))
	cmd.AddCommand(newFloatingIPShowCommand(a, o))
	cmd.AddCommand(newFloatingIPCreateCommand(a, o))
	cmd.AddCommand(newFloatingIPDeleteCommand(a, o))
	cmd.AddCommand(newFloatingIPSetCommand(a, o))
	cmd.AddCommand(newFloatingIPUnsetCommand(a, o))
	return cmd
}

func floatingIPShowFields(f *floatingips.FloatingIP) ([]string, []any) {
	fields := []string{
		"id", "floating_ip_address", "floating_network_id", "fixed_ip_address",
		"port_id", "router_id", "status", "description", "project_id",
		"created_at", "updated_at",
	}
	values := []any{
		f.ID, f.FloatingIP, f.FloatingNetworkID, f.FixedIP,
		f.PortID, f.RouterID, f.Status, f.Description, f.ProjectID,
		f.CreatedAt, f.UpdatedAt,
	}
	return fields, values
}

// resolveFloatingIPID resolves a floating IP address or ID to an ID. A single
// address match wins; otherwise the argument is assumed to be an ID.
func resolveFloatingIPID(ctx context.Context, client *gophercloud.ServiceClient, addrOrID string) (string, error) {
	pages, err := floatingips.List(client, floatingips.ListOpts{FloatingIP: addrOrID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up floating IP %q: %w", addrOrID, err)
	}
	all, err := floatingips.ExtractFloatingIPs(pages)
	if err != nil {
		return "", fmt.Errorf("parsing floating IP lookup for %q: %w", addrOrID, err)
	}
	return pickID(addrOrID, len(all), func(i int) string { return all[i].ID }, "floating IP")
}

func newFloatingIPListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List floating IPs",
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
			return runFloatingIPList(ctx, client, o, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runFloatingIPList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := floatingips.List(client, floatingips.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing floating IPs: %w", err)
	}
	all, err := floatingips.ExtractFloatingIPs(pages)
	if err != nil {
		return fmt.Errorf("parsing floating IP list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Floating IP Address", "Fixed IP Address", "Port", "Status"}, Rows: make([][]any, 0, len(all))}
	for _, f := range all {
		t.Rows = append(t.Rows, []any{f.ID, f.FloatingIP, f.FixedIP, f.PortID, f.Status})
	}
	return o.WriteList(w, t)
}

func newFloatingIPShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <floating-ip>",
		Short: "Show details of a floating IP",
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
			return runFloatingIPShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runFloatingIPShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, addrOrID string, w io.Writer) error {
	id, err := resolveFloatingIPID(ctx, client, addrOrID)
	if err != nil {
		return err
	}
	f, err := floatingips.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting floating IP %s: %w", addrOrID, err)
	}
	fields, values := floatingIPShowFields(f)
	return o.WriteSingle(w, fields, values)
}

type floatingIPCreateFlags struct {
	floatingIPAddress string
	subnet            string
	description       string
	port              string
	fixedIPAddr       string
}

func newFloatingIPCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &floatingIPCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <network>",
		Short: "Create a floating IP on an external network",
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
			return runFloatingIPCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.floatingIPAddress, "floating-ip-address", "", "specific floating IP address to allocate")
	fl.StringVar(&f.subnet, "subnet", "", "subnet on which to allocate the floating IP (name or ID)")
	fl.StringVar(&f.description, "description", "", "description for the floating IP")
	fl.StringVar(&f.port, "port", "", "port to associate the floating IP with at creation (name or ID)")
	fl.StringVar(&f.fixedIPAddr, "fixed-ip-address", "", "fixed IP of the associated port to bind the floating IP to")
	return cmd
}

func runFloatingIPCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, networkArg string, f *floatingIPCreateFlags, w io.Writer) error {
	networkID, err := resolveNetworkID(ctx, client, networkArg)
	if err != nil {
		return err
	}
	opts := floatingips.CreateOpts{
		FloatingNetworkID: networkID,
		FloatingIP:        f.floatingIPAddress,
		Description:       f.description,
		FixedIP:           f.fixedIPAddr,
	}
	if f.subnet != "" {
		subnetID, err := resolveSubnetID(ctx, client, f.subnet)
		if err != nil {
			return err
		}
		opts.SubnetID = subnetID
	}
	if f.port != "" {
		portID, err := resolvePortID(ctx, client, f.port)
		if err != nil {
			return err
		}
		opts.PortID = portID
	}
	fip, err := floatingips.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating floating IP: %w", err)
	}
	fields, values := floatingIPShowFields(fip)
	return o.WriteSingle(w, fields, values)
}

func newFloatingIPDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <floating-ip> [<floating-ip> ...]",
		Short: "Delete floating IP(s)",
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
			return runFloatingIPDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runFloatingIPDelete(ctx context.Context, client *gophercloud.ServiceClient, addrs []string, w io.Writer) error {
	var errs []error
	for _, addrOrID := range addrs {
		id, err := resolveFloatingIPID(ctx, client, addrOrID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := floatingips.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting floating IP %s: %w", addrOrID, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted floating IP %s\n", addrOrID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type floatingIPSetFlags struct {
	port        string
	fixedIPAddr string
}

func newFloatingIPSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &floatingIPSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <floating-ip>",
		Short: "Associate a floating IP with a port",
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
			return runFloatingIPSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.port, "port", "", "port to associate with the floating IP (name or ID)")
	fl.StringVar(&f.fixedIPAddr, "fixed-ip-address", "", "fixed IP of the port to associate")
	return cmd
}

func runFloatingIPSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, addrOrID string, f *floatingIPSetFlags, w io.Writer) error {
	id, err := resolveFloatingIPID(ctx, client, addrOrID)
	if err != nil {
		return err
	}
	if f.port == "" {
		return fmt.Errorf("floating ip set requires --port")
	}
	portID, err := resolvePortID(ctx, client, f.port)
	if err != nil {
		return err
	}
	opts := floatingips.UpdateOpts{PortID: &portID, FixedIP: f.fixedIPAddr}
	fip, err := floatingips.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("associating floating IP %s: %w", addrOrID, err)
	}
	fields, values := floatingIPShowFields(fip)
	return o.WriteSingle(w, fields, values)
}

func newFloatingIPUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var port bool
	cmd := &cobra.Command{
		Use:   "unset <floating-ip>",
		Short: "Disassociate a floating IP from its port",
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
			return runFloatingIPUnset(ctx, client, o, args[0], port, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&port, "port", false, "disassociate the floating IP from its port")
	return cmd
}

func runFloatingIPUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, addrOrID string, port bool, w io.Writer) error {
	id, err := resolveFloatingIPID(ctx, client, addrOrID)
	if err != nil {
		return err
	}
	if !port {
		return fmt.Errorf("floating ip unset requires --port")
	}
	// A nil PortID marshals to null via ToFloatingIPUpdateMap, disassociating.
	empty := ""
	opts := floatingips.UpdateOpts{PortID: &empty}
	fip, err := floatingips.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("disassociating floating IP %s: %w", addrOrID, err)
	}
	fields, values := floatingIPShowFields(fip)
	return o.WriteSingle(w, fields, values)
}
