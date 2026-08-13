package loadbalancer

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/nameflag"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func lbFields(lb *loadbalancers.LoadBalancer) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "provisioning_status", "operating_status",
		"admin_state_up", "project_id", "vip_address", "vip_port_id",
		"vip_subnet_id", "vip_network_id", "vip_qos_policy_id", "provider",
		"flavor_id", "availability_zone", "tags", "created_at", "updated_at",
	}
	values := []any{
		lb.ID, lb.Name, lb.Description, lb.ProvisioningStatus, lb.OperatingStatus,
		lb.AdminStateUp, lb.ProjectID, lb.VipAddress, lb.VipPortID,
		lb.VipSubnetID, lb.VipNetworkID, lb.VipQosPolicyID, lb.Provider,
		lb.FlavorID, lb.AvailabilityZone, lb.Tags, lb.CreatedAt, lb.UpdatedAt,
	}
	return fields, values
}

// --- list ------------------------------------------------------------------

type lbListFlags struct {
	name               string
	project            string
	projectDomain      string
	provisioningStatus string
	operatingStatus    string
	vipAddress         string
	vipSubnet          string
	vipNetwork         string
	provider           string
	availabilityZone   string
	flavor             string
	enable             bool
	disable            bool
	long               bool

	adminStateUp *bool
}

func newLBListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &lbListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List load balancers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			refs, err := resolveLBRefs(ctx, session, lbRefs{
				project:       f.project,
				projectDomain: f.projectDomain,
				vipSubnet:     f.vipSubnet,
				vipNetwork:    f.vipNetwork,
			})
			if err != nil {
				return err
			}
			return runLBList(ctx, client, o, f, refs, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by load balancer name")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.StringVar(&f.provisioningStatus, "provisioning-status", "", "filter by provisioning status, e.g. ACTIVE or ERROR")
	fl.StringVar(&f.operatingStatus, "operating-status", "", "filter by operating status, e.g. ONLINE or OFFLINE")
	fl.StringVar(&f.vipAddress, "vip-address", "", "filter by VIP address")
	fl.StringVar(&f.vipSubnet, "vip-subnet-id", "", "filter by VIP subnet (name or ID)")
	fl.StringVar(&f.vipNetwork, "vip-network-id", "", "filter by VIP network (name or ID)")
	fl.StringVar(&f.provider, "provider", "", "filter by provider driver, e.g. amphora or ovn")
	fl.StringVar(&f.availabilityZone, "availability-zone", "", "filter by availability zone")
	fl.StringVar(&f.flavor, "flavor", "", "filter by octavia flavor ID")
	fl.BoolVar(&f.enable, "enable", false, "list only administratively up load balancers")
	fl.BoolVar(&f.disable, "disable", false, "list only administratively down load balancers")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runLBList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *lbListFlags, refs resolvedLBRefs, w io.Writer,
) error {
	opts := loadbalancers.ListOpts{
		Name:               f.name,
		ProjectID:          refs.projectID,
		ProvisioningStatus: f.provisioningStatus,
		OperatingStatus:    f.operatingStatus,
		VipAddress:         f.vipAddress,
		VipSubnetID:        refs.vipSubnetID,
		VipNetworkID:       refs.vipNetworkID,
		Provider:           f.provider,
		AvailabilityZone:   f.availabilityZone,
		FlavorID:           f.flavor,
		AdminStateUp:       f.adminStateUp,
	}
	pages, err := loadbalancers.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing load balancers: %w", err)
	}
	all, err := loadbalancers.ExtractLoadBalancers(pages)
	if err != nil {
		return fmt.Errorf("parsing load balancer list: %w", err)
	}

	cols := []string{"ID", "Name", "Project", "VIP Address", "Provisioning Status", "Operating Status", "Provider"}
	if f.long {
		cols = append(cols, "Admin State Up", "VIP Subnet", "Availability Zone", "Flavor", "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, lb := range all {
		row := []any{lb.ID, lb.Name, lb.ProjectID, lb.VipAddress, lb.ProvisioningStatus, lb.OperatingStatus, lb.Provider}
		if f.long {
			row = append(row, lb.AdminStateUp, lb.VipSubnetID, lb.AvailabilityZone, lb.FlavorID, lb.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newLBShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <load-balancer>",
		Short: "Show load balancer details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runLBShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveLoadBalancerID(ctx, client, ref)
	if err != nil {
		return err
	}
	lb, err := loadbalancers.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing load balancer %q: %w", ref, err)
	}
	fields, values := lbFields(lb)
	return o.WriteSingle(w, fields, values)
}

// --- create ----------------------------------------------------------------

type lbCreateFlags struct {
	name             string
	description      string
	vipSubnet        string
	vipNetwork       string
	vipPort          string
	vipAddress       string
	vipQosPolicy     string
	project          string
	projectDomain    string
	provider         string
	flavor           string
	availabilityZone string
	tag              []string
	enable           bool
	disable          bool
	wait             bool
	waitTimeout      time.Duration

	adminStateUp *bool
}

func newLBCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &lbCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a new load balancer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, f.name, false)
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			// Octavia needs exactly one VIP anchor.
			given := 0
			for _, v := range []string{f.vipSubnet, f.vipNetwork, f.vipPort} {
				if v != "" {
					given++
				}
			}
			if given != 1 {
				return fmt.Errorf("exactly one of --vip-subnet-id, --vip-network-id or --vip-port-id is required")
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			refs, err := resolveLBRefs(ctx, session, lbRefs{
				project:       f.project,
				projectDomain: f.projectDomain,
				vipSubnet:     f.vipSubnet,
				vipNetwork:    f.vipNetwork,
				vipPort:       f.vipPort,
			})
			if err != nil {
				return err
			}
			return runLBCreate(ctx, client, o, name, f, refs, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Upstream octavia names a new load balancer with --name and has no positional
	// for it; koc grew the positional first. Both work — see internal/cli/nameflag.
	fl.StringVar(&f.name, "name", "", "name of the load balancer (upstream spelling; the positional form also works)")
	fl.StringVar(&f.description, "description", "", "load balancer description")
	fl.StringVar(&f.vipSubnet, "vip-subnet-id", "", "subnet to allocate the VIP from (name or ID)")
	fl.StringVar(&f.vipNetwork, "vip-network-id", "", "network to allocate the VIP from (name or ID)")
	fl.StringVar(&f.vipPort, "vip-port-id", "", "existing neutron port to use as the VIP (name or ID)")
	fl.StringVar(&f.vipAddress, "vip-address", "", "specific IP address to use for the VIP")
	fl.StringVar(&f.vipQosPolicy, "vip-qos-policy-id", "", "QoS policy ID to apply to the VIP port")
	fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.StringVar(&f.provider, "provider", "", "provider driver, e.g. amphora or ovn")
	fl.StringVar(&f.flavor, "flavor", "", "octavia flavor ID")
	fl.StringVar(&f.availabilityZone, "availability-zone", "", "availability zone to create the load balancer in")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set on the load balancer (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "create the load balancer administratively up (the default)")
	fl.BoolVar(&f.disable, "disable", false, "create the load balancer administratively down")
	fl.BoolVar(&f.wait, "wait", false, "wait until the load balancer reaches ACTIVE")
	fl.DurationVar(&f.waitTimeout, "wait-timeout", provisioningPollTimeout, "maximum time to wait for --wait to complete")
	return cmd
}

func runLBCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *lbCreateFlags, refs resolvedLBRefs, w io.Writer,
) error {
	opts := loadbalancers.CreateOpts{
		Name:             name,
		Description:      f.description,
		VipSubnetID:      refs.vipSubnetID,
		VipNetworkID:     refs.vipNetworkID,
		VipPortID:        refs.vipPortID,
		VipAddress:       f.vipAddress,
		VipQosPolicyID:   f.vipQosPolicy,
		ProjectID:        refs.projectID,
		Provider:         f.provider,
		FlavorID:         f.flavor,
		AvailabilityZone: f.availabilityZone,
		Tags:             f.tag,
		AdminStateUp:     f.adminStateUp,
	}
	lb, err := loadbalancers.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating load balancer %q: %w", name, err)
	}
	if f.wait {
		if werr := waitForLoadBalancerActive(ctx, client, lb.ID, f.waitTimeout); werr != nil {
			// The load balancer exists — octavia accepted the create — so it has
			// to reach stdout before the error does, or its ID survives only
			// inside the error string and the operator has to scrape it out to
			// clean up. Rendered here rather than unconditionally before the wait
			// so the success path still emits exactly one record, which is what
			// -f json and -f yaml consumers require.
			fields, values := lbFields(lb)
			if perr := o.WriteSingle(w, fields, values); perr != nil {
				return perr
			}
			return werr
		}
		lb, err = loadbalancers.Get(ctx, client, lb.ID).Extract()
		if err != nil {
			return fmt.Errorf("getting load balancer %s after --wait: %w", lb.ID, err)
		}
	}
	fields, values := lbFields(lb)
	return o.WriteSingle(w, fields, values)
}

// --- set -------------------------------------------------------------------

type lbSetFlags struct {
	name         string
	description  string
	vipQosPolicy string
	tag          []string
	noTag        bool
	enable       bool
	disable      bool

	adminStateUp *bool
}

func newLBSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &lbSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <load-balancer>",
		Short: "Update a load balancer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if fl.Changed("tag") && f.noTag {
				return fmt.Errorf("--tag and --no-tag are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBSet(ctx, client, o, args[0], f, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new load balancer name")
	fl.StringVar(&f.description, "description", "", "new load balancer description")
	fl.StringVar(&f.vipQosPolicy, "vip-qos-policy-id", "", "QoS policy ID to apply to the VIP port")
	fl.StringArrayVar(&f.tag, "tag", nil, "replace the tags with these (repeatable)")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
	fl.BoolVar(&f.enable, "enable", false, "set the load balancer administratively up")
	fl.BoolVar(&f.disable, "disable", false, "set the load balancer administratively down")
	return cmd
}

// runLBSet builds a sparse UpdateOpts: every field is a pointer, so only the
// attributes whose flags were given are sent and an unrelated `set --name x`
// cannot clear the description or the tags.
func runLBSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *lbSetFlags, changed changedSet, w io.Writer,
) error {
	opts := loadbalancers.UpdateOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil
	if changed["name"] {
		name := f.name
		opts.Name = &name
		touched = true
	}
	if changed["description"] {
		desc := f.description
		opts.Description = &desc
		touched = true
	}
	if changed["vip-qos-policy-id"] {
		policy := f.vipQosPolicy
		opts.VipQosPolicyID = &policy
		touched = true
	}
	switch {
	case f.noTag:
		empty := []string{}
		opts.Tags = &empty
		touched = true
	case changed["tag"]:
		tags := f.tag
		opts.Tags = &tags
		touched = true
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}

	// Resolved after the emptiness check so a no-op invocation costs no round trip.
	id, err := resolveLoadBalancerID(ctx, client, ref)
	if err != nil {
		return err
	}
	lb, err := loadbalancers.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating load balancer %q: %w", ref, err)
	}
	fields, values := lbFields(lb)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newLBDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var cascade, wait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "delete <load-balancer> [<load-balancer>...]",
		Short: "Delete one or more load balancers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBDelete(ctx, client, args, cascade, wait, waitTimeout, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&cascade, "cascade", false, "also delete the load balancer's listeners, pools, members and monitors")
	fl.BoolVar(&wait, "wait", false, "wait until each load balancer is gone")
	fl.DurationVar(&waitTimeout, "wait-timeout", provisioningPollTimeout, "maximum time to wait for --wait to complete")
	return cmd
}

func runLBDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string,
	cascade, wait bool, waitTimeout time.Duration, w io.Writer,
) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveLoadBalancerID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := loadbalancers.Delete(ctx, client, id, loadbalancers.DeleteOpts{Cascade: cascade}).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting load balancer %q: %w", ref, derr)
		}
		if wait {
			if werr := waitForLoadBalancerDeleted(ctx, client, id, waitTimeout); werr != nil {
				return werr
			}
			if _, werr := fmt.Fprintf(w, "Deleted load balancer %s\n", ref); werr != nil {
				return werr
			}
			return nil
		}
		// Without --wait the delete is only accepted, not finished: octavia leaves
		// the record in PENDING_DELETE and rejects further writes until it settles.
		if _, werr := fmt.Fprintf(w, "Requested deletion of load balancer %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

// --- failover --------------------------------------------------------------

func newLBFailoverCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var wait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "failover <load-balancer>",
		Short: "Trigger a load balancer failover",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBFailover(ctx, client, args[0], wait, waitTimeout, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&wait, "wait", false, "wait until the load balancer is ACTIVE again")
	fl.DurationVar(&waitTimeout, "wait-timeout", provisioningPollTimeout, "maximum time to wait for --wait to complete")
	return cmd
}

func runLBFailover(ctx context.Context, client *gophercloud.ServiceClient, ref string,
	wait bool, waitTimeout time.Duration, w io.Writer,
) error {
	id, err := resolveLoadBalancerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := loadbalancers.Failover(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("failing over load balancer %q: %w", ref, err)
	}
	if wait {
		if err := waitForLoadBalancerActive(ctx, client, id, waitTimeout); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Load balancer %s failed over and is %s\n", ref, statusActive); err != nil {
			return err
		}
		return nil
	}
	if _, err := fmt.Fprintf(w, "Requested failover of load balancer %s\n", ref); err != nil {
		return err
	}
	return nil
}

// --- stats show / status show ----------------------------------------------

func newLBStatsCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "stats", Short: "Load balancer traffic statistics"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show <load-balancer>",
		Short: "Show a load balancer's traffic statistics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBStatsShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	return cmd
}

func runLBStatsShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveLoadBalancerID(ctx, client, ref)
	if err != nil {
		return err
	}
	stats, err := loadbalancers.GetStats(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting statistics for load balancer %q: %w", ref, err)
	}
	return o.WriteSingle(w,
		[]string{"active_connections", "bytes_in", "bytes_out", "request_errors", "total_connections"},
		[]any{stats.ActiveConnections, stats.BytesIn, stats.BytesOut, stats.RequestErrors, stats.TotalConnections})
}

func newLBStatusCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "status", Short: "Load balancer status tree"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show <load-balancer>",
		Short: "Show the status of a load balancer and everything under it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBStatusShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	return cmd
}

// runLBStatusShow flattens octavia's nested status tree into one row per object.
// The API returns loadbalancer → listeners → pools → members/healthmonitor, and a
// table of (type, name, id, provisioning, operating) is what an operator is
// actually looking for when something is OFFLINE.
func runLBStatusShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveLoadBalancerID(ctx, client, ref)
	if err != nil {
		return err
	}
	tree, err := loadbalancers.GetStatuses(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting status tree for load balancer %q: %w", ref, err)
	}
	if tree.Loadbalancer == nil {
		return fmt.Errorf("octavia returned no status tree for load balancer %q", ref)
	}

	t := output.Table{
		Columns: []string{"Type", "Name", "ID", "Provisioning Status", "Operating Status"},
	}
	lb := tree.Loadbalancer
	t.Rows = append(t.Rows, []any{"loadbalancer", lb.Name, lb.ID, lb.ProvisioningStatus, lb.OperatingStatus})
	for _, l := range lb.Listeners {
		t.Rows = append(t.Rows, []any{"listener", l.Name, l.ID, l.ProvisioningStatus, l.OperatingStatus})
		for _, p := range l.Pools {
			t.Rows = append(t.Rows, []any{"pool", p.Name, p.ID, p.ProvisioningStatus, p.OperatingStatus})
			for _, m := range p.Members {
				t.Rows = append(t.Rows, []any{"member", m.Name, m.ID, m.ProvisioningStatus, m.OperatingStatus})
			}
			if p.Monitor.ID != "" {
				t.Rows = append(t.Rows, []any{"healthmonitor", p.Monitor.Name, p.Monitor.ID,
					p.Monitor.ProvisioningStatus, p.Monitor.OperatingStatus})
			}
		}
	}
	return o.WriteList(w, t)
}
