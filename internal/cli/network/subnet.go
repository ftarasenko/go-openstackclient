package network

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newSubnetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subnet",
		Short: "Manage subnets",
	}
	cmd.AddCommand(newSubnetListCommand(a, o))
	cmd.AddCommand(newSubnetShowCommand(a, o))
	cmd.AddCommand(newSubnetCreateCommand(a, o))
	cmd.AddCommand(newSubnetDeleteCommand(a, o))
	cmd.AddCommand(newSubnetSetCommand(a, o))
	cmd.AddCommand(newSubnetPoolCommand(a, o))
	return cmd
}

func subnetShowFields(s *subnets.Subnet) ([]string, []any) {
	fields := []string{
		"id", "name", "network_id", "cidr", "ip_version", "gateway_ip",
		"enable_dhcp", "dns_nameservers", "allocation_pools", "host_routes",
		"description", "project_id", "tags", "created_at", "updated_at",
	}
	values := []any{
		s.ID, s.Name, s.NetworkID, s.CIDR, s.IPVersion, s.GatewayIP,
		s.EnableDHCP, s.DNSNameservers, s.AllocationPools, s.HostRoutes,
		s.Description, s.ProjectID, s.Tags, s.CreatedAt, s.UpdatedAt,
	}
	return fields, values
}

// subnetListFlags holds the filters accepted by "subnet list". Until this pass
// the command took no filters at all beyond --help, so every one of these is new.
type subnetListFlags struct {
	name          string
	network       string
	project       string
	projectDomain string
	subnetPool    string
	gateway       string
	ipVersion     int
	dhcp          bool
	noDHCP        bool
	long          bool

	enableDHCP *bool
}

func newSubnetListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subnets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, "dhcp", "no-dhcp"); err != nil {
				return err
			}
			f.enableDHCP = enableDisable(fl, f.dhcp, f.noDHCP, "dhcp", "no-dhcp")
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runSubnetList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "list subnets matching this name")
	fl.StringVar(&f.network, "network", "", "list subnets of this network (name or ID)")
	fl.StringVar(&f.project, "project", "", "list subnets owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.StringVar(&f.subnetPool, "subnet-pool", "", "list subnets allocated from this subnet pool (name or ID)")
	fl.StringVar(&f.gateway, "gateway", "", "list subnets with this gateway IP")
	fl.IntVar(&f.ipVersion, "ip-version", 0, "list subnets of this IP version (4 or 6)")
	fl.BoolVar(&f.dhcp, "dhcp", false, "list only subnets with DHCP enabled")
	fl.BoolVar(&f.noDHCP, "no-dhcp", false, "list only subnets with DHCP disabled")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runSubnetList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *subnetListFlags, projectID string, w io.Writer,
) error {
	opts := subnets.ListOpts{
		Name:       f.name,
		ProjectID:  projectID,
		GatewayIP:  f.gateway,
		IPVersion:  f.ipVersion,
		EnableDHCP: f.enableDHCP,
	}
	if f.network != "" {
		networkID, err := resolveNetworkID(ctx, client, f.network)
		if err != nil {
			return err
		}
		opts.NetworkID = networkID
	}
	if f.subnetPool != "" {
		poolID, err := resolveSubnetPoolID(ctx, client, f.subnetPool)
		if err != nil {
			return err
		}
		opts.SubnetPoolID = poolID
	}
	pages, err := subnets.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing subnets: %w", err)
	}
	all, err := subnets.ExtractSubnets(pages)
	if err != nil {
		return fmt.Errorf("parsing subnet list: %w", err)
	}

	cols := []string{"ID", "Name", "Network", "Subnet"}
	if f.long {
		cols = append(cols, "Project", "DHCP", "Gateway", "IP Version", "Subnet Pool", "Allocation Pools")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		row := []any{s.ID, s.Name, s.NetworkID, s.CIDR}
		if f.long {
			row = append(row, s.ProjectID, s.EnableDHCP, s.GatewayIP, s.IPVersion, s.SubnetPoolID, formatAllocationPools(s.AllocationPools))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// formatAllocationPools renders a subnet's allocation pools as "start-end"
// entries so the range fits one table cell.
func formatAllocationPools(pools []subnets.AllocationPool) []string {
	out := make([]string, 0, len(pools))
	for _, p := range pools {
		out = append(out, p.Start+"-"+p.End)
	}
	return out
}

func newSubnetShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <subnet>",
		Short: "Show details of a subnet",
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
			return runSubnetShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSubnetShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveSubnetID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	s, err := subnets.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting subnet %s: %w", nameOrID, err)
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

type subnetCreateFlags struct {
	network        string
	subnetRange    string
	ipVersion      int
	gateway        string
	dhcp           bool
	noDHCP         bool
	dnsNameservers []string
	allocationPool []string
}

func newSubnetCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new subnet",
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
			return runSubnetCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.network, "network", "", "network this subnet belongs to (name or ID, required)")
	fl.StringVar(&f.subnetRange, "subnet-range", "", "subnet CIDR range (e.g. 10.0.0.0/24)")
	fl.IntVar(&f.ipVersion, "ip-version", 4, "IP version: 4 or 6")
	fl.StringVar(&f.gateway, "gateway", "", "subnet gateway IP address")
	fl.BoolVar(&f.dhcp, "dhcp", false, "enable DHCP (default)")
	fl.BoolVar(&f.noDHCP, "no-dhcp", false, "disable DHCP")
	fl.StringArrayVar(&f.dnsNameservers, "dns-nameserver", nil, "DNS nameserver (repeatable)")
	fl.StringArrayVar(&f.allocationPool, "allocation-pool", nil, "allocation pool as start=<ip>,end=<ip> (repeatable)")
	_ = cmd.MarkFlagRequired("network")
	return cmd
}

func runSubnetCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *subnetCreateFlags, w io.Writer) error {
	networkID, err := resolveNetworkID(ctx, client, f.network)
	if err != nil {
		return err
	}
	opts := subnets.CreateOpts{
		NetworkID:      networkID,
		Name:           name,
		CIDR:           f.subnetRange,
		IPVersion:      gophercloud.IPVersion(f.ipVersion),
		DNSNameservers: f.dnsNameservers,
	}
	if f.gateway != "" {
		opts.GatewayIP = &f.gateway
	}
	switch {
	case f.noDHCP:
		opts.EnableDHCP = boolPtr(false)
	case f.dhcp:
		opts.EnableDHCP = boolPtr(true)
	}
	for _, spec := range f.allocationPool {
		p, err := parseAllocationPool(spec)
		if err != nil {
			return err
		}
		opts.AllocationPools = append(opts.AllocationPools, p)
	}
	s, err := subnets.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating subnet: %w", err)
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func newSubnetDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <subnet> [<subnet> ...]",
		Short: "Delete subnet(s)",
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
			return runSubnetDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSubnetDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	var errs []error
	for _, nameOrID := range names {
		id, err := resolveSubnetID(ctx, client, nameOrID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := subnets.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting subnet %s: %w", nameOrID, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted subnet %s\n", nameOrID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type subnetSetFlags struct {
	name           string
	dnsNameservers []string
	gateway        string
	noGateway      bool
	dhcp           bool
	noDHCP         bool
}

func newSubnetSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <subnet>",
		Short: "Set subnet properties",
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
			return runSubnetSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new subnet name")
	fl.StringArrayVar(&f.dnsNameservers, "dns-nameserver", nil, "set DNS nameserver(s), replacing existing (repeatable)")
	fl.StringVar(&f.gateway, "gateway", "", "set the subnet gateway IP")
	fl.BoolVar(&f.noGateway, "no-gateway", false, "clear the subnet gateway IP")
	fl.BoolVar(&f.dhcp, "dhcp", false, "enable DHCP")
	fl.BoolVar(&f.noDHCP, "no-dhcp", false, "disable DHCP")
	cmd.MarkFlagsMutuallyExclusive("dhcp", "no-dhcp")
	cmd.MarkFlagsMutuallyExclusive("gateway", "no-gateway")
	return cmd
}

func runSubnetSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *subnetSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolveSubnetID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts := subnets.UpdateOpts{}
	changed := false
	if f.name != "" {
		opts.Name = &f.name
		changed = true
	}
	if flags.Changed("dns-nameserver") {
		opts.DNSNameservers = &f.dnsNameservers
		changed = true
	}
	switch {
	case f.noGateway:
		// A pointer to the empty string clears the gateway (neutron drops it).
		empty := ""
		opts.GatewayIP = &empty
		changed = true
	case f.gateway != "":
		opts.GatewayIP = &f.gateway
		changed = true
	}
	switch {
	case f.noDHCP:
		opts.EnableDHCP = boolPtr(false)
		changed = true
	case f.dhcp:
		opts.EnableDHCP = boolPtr(true)
		changed = true
	}
	if !changed {
		return fmt.Errorf("subnet set requires at least one attribute flag")
	}
	s, err := subnets.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating subnet %s: %w", nameOrID, err)
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}
