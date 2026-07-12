package network

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
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
	return cmd
}

func portShowFields(p *ports.Port) ([]string, []any) {
	fields := []string{
		"id", "name", "network_id", "mac_address", "status", "admin_state_up",
		"device_owner", "device_id", "fixed_ips", "security_groups",
		"description", "project_id", "tags", "created_at", "updated_at",
	}
	values := []any{
		p.ID, p.Name, p.NetworkID, p.MACAddress, p.Status, p.AdminStateUp,
		p.DeviceOwner, p.DeviceID, p.FixedIPs, p.SecurityGroups,
		p.Description, p.ProjectID, p.Tags, p.CreatedAt, p.UpdatedAt,
	}
	return fields, values
}

type portListFlags struct {
	router      string
	network     string
	deviceOwner string
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
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.router, "router", "", "list only ports attached to this router (name or ID)")
	fl.StringVar(&f.network, "network", "", "list only ports on this network (name or ID)")
	fl.StringVar(&f.deviceOwner, "device-owner", "", "list only ports with this device owner")
	return cmd
}

func runPortList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portListFlags, w io.Writer) error {
	opts := ports.ListOpts{DeviceOwner: f.deviceOwner}
	if f.router != "" {
		routerID, err := resolveRouterID(ctx, client, f.router)
		if err != nil {
			return err
		}
		opts.DeviceID = routerID
	}
	if f.network != "" {
		networkID, err := resolveNetworkID(ctx, client, f.network)
		if err != nil {
			return err
		}
		opts.NetworkID = networkID
	}
	pages, err := ports.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing ports: %w", err)
	}
	all, err := ports.ExtractPorts(pages)
	if err != nil {
		return fmt.Errorf("parsing port list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "MAC Address", "Fixed IP Addresses", "Status"}, Rows: make([][]any, 0, len(all))}
	for i := range all {
		p := &all[i]
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.MACAddress, p.FixedIPs, p.Status})
	}
	return o.WriteList(w, t)
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
	p, err := ports.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting port %s: %w", nameOrID, err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

type portCreateFlags struct {
	network       string
	fixedIP       []string
	macAddress    string
	deviceOwner   string
	description   string
	securityGroup []string
	enable        bool
	disable       bool
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
			return runPortCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.network, "network", "", "network for the port (name or ID, required)")
	fl.StringArrayVar(&f.fixedIP, "fixed-ip", nil, "desired IP as subnet=<name|id>,ip-address=<ip> (repeatable)")
	fl.StringVar(&f.macAddress, "mac-address", "", "MAC address for the port")
	fl.StringVar(&f.deviceOwner, "device-owner", "", "device owner for the port")
	fl.StringVar(&f.description, "description", "", "description for the port")
	fl.StringArrayVar(&f.securityGroup, "security-group", nil, "security group to associate (name or ID, repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "create the port administratively up (default)")
	fl.BoolVar(&f.disable, "disable", false, "create the port administratively down")
	_ = cmd.MarkFlagRequired("network")
	return cmd
}

func runPortCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *portCreateFlags, w io.Writer) error {
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
	if len(f.securityGroup) > 0 {
		sgIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return err
		}
		opts.SecurityGroups = &sgIDs
	}
	p, err := ports.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating port: %w", err)
	}
	fields, values := portShowFields(p)
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
	var errs []error
	for _, nameOrID := range names {
		id, err := resolvePortID(ctx, client, nameOrID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := ports.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting port %s: %w", nameOrID, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted port %s\n", nameOrID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type portSetFlags struct {
	name            string
	fixedIP         []string
	description     string
	securityGroup   []string
	noSecurityGroup bool
	enable          bool
	disable         bool
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
	fl.StringArrayVar(&f.fixedIP, "fixed-ip", nil, "desired IP as subnet=<name|id>,ip-address=<ip> (repeatable, replaces existing)")
	fl.StringVar(&f.description, "description", "", "new port description")
	fl.StringArrayVar(&f.securityGroup, "security-group", nil, "security group to associate (name or ID, repeatable, replaces existing)")
	fl.BoolVar(&f.noSecurityGroup, "no-security-group", false, "clear all security groups from the port")
	fl.BoolVar(&f.enable, "enable", false, "set the port administratively up")
	fl.BoolVar(&f.disable, "disable", false, "set the port administratively down")
	cmd.MarkFlagsMutuallyExclusive("security-group", "no-security-group")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
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
	if flags.Changed("fixed-ip") {
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
	case flags.Changed("security-group"):
		sgIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return err
		}
		opts.SecurityGroups = &sgIDs
		changed = true
	}
	if !changed {
		return fmt.Errorf("port set requires at least one attribute flag")
	}
	p, err := ports.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating port %s: %w", nameOrID, err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}
