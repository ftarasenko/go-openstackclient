package loadbalancer

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/pools"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/nameflag"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newMemberCommand builds "loadbalancer member ...".
//
// Members are pool subresources in octavia — every verb is scoped to a pool — so
// the pool is the first positional argument throughout, matching upstream.
func newMemberCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "member",
		Short: "Manage the members of a load balancer pool",
	}
	cmd.AddCommand(
		newMemberListCommand(a, o),
		newMemberShowCommand(a, o),
		newMemberCreateCommand(a, o),
		newMemberSetCommand(a, o),
		newMemberDeleteCommand(a, o),
	)
	return cmd
}

func memberFields(m *pools.Member) ([]string, []any) {
	fields := []string{
		"id", "name", "address", "protocol_port", "weight", "backup", "subnet_id",
		"monitor_address", "monitor_port", "admin_state_up", "project_id",
		"provisioning_status", "operating_status", "tags", "created_at", "updated_at",
	}
	values := []any{
		m.ID, m.Name, m.Address, m.ProtocolPort, m.Weight, m.Backup, m.SubnetID,
		m.MonitorAddress, m.MonitorPort, m.AdminStateUp, m.ProjectID,
		m.ProvisioningStatus, m.OperatingStatus, m.Tags, m.CreatedAt, m.UpdatedAt,
	}
	return fields, values
}

// resolveMemberID turns a member name or ID into an ID within one pool. Octavia's
// member list has a name filter, but member names are not unique within a pool, so
// an ambiguous name is rejected rather than resolved arbitrarily.
func resolveMemberID(ctx context.Context, client *gophercloud.ServiceClient, poolID, ref string) (string, error) {
	return resolveByName("member", ref, func() ([]string, error) {
		pages, err := pools.ListMembers(client, poolID, pools.ListMembersOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := pools.ExtractMembers(pages)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, m := range all {
			ids = append(ids, m.ID)
		}
		return ids, nil
	})
}

// --- list ------------------------------------------------------------------

type memberListFlags struct {
	name         string
	address      string
	protocolPort int
	project      string
	enable       bool
	disable      bool
	long         bool

	adminStateUp *bool
}

func newMemberListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &memberListFlags{}
	cmd := &cobra.Command{
		Use:   "list <pool>",
		Short: "List the members of a pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			refs, err := resolveLBRefs(ctx, session, lbRefs{project: f.project})
			if err != nil {
				return err
			}
			return runMemberList(ctx, client, o, args[0], f, refs.projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by member name")
	fl.StringVar(&f.address, "address", "", "filter by member address")
	fl.IntVar(&f.protocolPort, "protocol-port", 0, "filter by member port")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only administratively up members")
	fl.BoolVar(&f.disable, "disable", false, "list only administratively down members")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runMemberList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	poolRef string, f *memberListFlags, projectID string, w io.Writer,
) error {
	poolID, err := resolvePoolID(ctx, client, poolRef)
	if err != nil {
		return err
	}
	opts := pools.ListMembersOpts{
		Name:         f.name,
		Address:      f.address,
		ProtocolPort: f.protocolPort,
		ProjectID:    projectID,
		AdminStateUp: f.adminStateUp,
	}
	pages, err := pools.ListMembers(client, poolID, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing members of pool %q: %w", poolRef, err)
	}
	all, err := pools.ExtractMembers(pages)
	if err != nil {
		return fmt.Errorf("parsing member list: %w", err)
	}

	cols := []string{"ID", "Name", "Address", "Protocol Port", "Weight", "Operating Status"}
	if f.long {
		cols = append(cols, "Provisioning Status", "Admin State Up", "Backup", "Subnet", "Monitor Address", "Monitor Port", "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, m := range all {
		row := []any{m.ID, m.Name, m.Address, m.ProtocolPort, m.Weight, m.OperatingStatus}
		if f.long {
			row = append(row, m.ProvisioningStatus, m.AdminStateUp, m.Backup, m.SubnetID,
				m.MonitorAddress, m.MonitorPort, m.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newMemberShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <pool> <member>",
		Short: "Show details of one pool member",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runMemberShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runMemberShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	poolRef, memberRef string, w io.Writer,
) error {
	poolID, err := resolvePoolID(ctx, client, poolRef)
	if err != nil {
		return err
	}
	memberID, err := resolveMemberID(ctx, client, poolID, memberRef)
	if err != nil {
		return err
	}
	m, err := pools.GetMember(ctx, client, poolID, memberID).Extract()
	if err != nil {
		return fmt.Errorf("showing member %q of pool %q: %w", memberRef, poolRef, err)
	}
	fields, values := memberFields(m)
	return o.WriteSingle(w, fields, values)
}

// --- create ----------------------------------------------------------------

type memberWriteFlags struct {
	name           string
	address        string
	protocolPort   int
	weight         int
	subnet         string
	monitorAddress string
	monitorPort    int
	backup         bool
	noBackup       bool
	tag            []string
	noTag          bool
	project        string
	enable         bool
	disable        bool

	adminStateUp *bool
}

func (f *memberWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.IntVar(&f.weight, "weight", 0, "relative share of traffic this member takes (0 disables it)")
	fl.StringVar(&f.monitorAddress, "monitor-address", "", "alternate address for health checks")
	fl.IntVar(&f.monitorPort, "monitor-port", 0, "alternate port for health checks")
	fl.BoolVar(&f.backup, "enable-backup", false, "make this a backup member, used only when the primaries are down")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "administratively up")
	fl.BoolVar(&f.disable, "disable", false, "administratively down")
	if isCreate {
		// Upstream octavia names a new member with --name and has no positional
		// for it; koc grew the positional first. Both work — see internal/cli/nameflag.
		fl.StringVar(&f.name, "name", "", "name of the member (upstream spelling; the positional form also works)")
		fl.StringVar(&f.address, "address", "", "member IP address (required)")
		fl.IntVar(&f.protocolPort, "protocol-port", 0, "member port (required)")
		fl.StringVar(&f.subnet, "subnet-id", "", "subnet the member address belongs to (name or ID)")
		fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
		return
	}
	fl.StringVar(&f.name, "name", "", "new member name")
	fl.BoolVar(&f.noBackup, "disable-backup", false, "stop treating this as a backup member")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
}

func newMemberCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &memberWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <pool> [<name>]",
		Short: "Add a member to a pool",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args[1:], f.name, false)
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			if f.address == "" || f.protocolPort == 0 {
				return fmt.Errorf("--address and --protocol-port are required")
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			refs, err := resolveLBRefs(ctx, session, lbRefs{project: f.project, vipSubnet: f.subnet})
			if err != nil {
				return err
			}
			return runMemberCreate(ctx, client, o, args[0], name, f, refs, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

func runMemberCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	poolRef, name string, f *memberWriteFlags, refs resolvedLBRefs, changed changedSet, w io.Writer,
) error {
	poolID, err := resolvePoolID(ctx, client, poolRef)
	if err != nil {
		return err
	}
	opts := pools.CreateMemberOpts{
		Name:           name,
		Address:        f.address,
		ProtocolPort:   f.protocolPort,
		SubnetID:       refs.vipSubnetID,
		ProjectID:      refs.projectID,
		MonitorAddress: f.monitorAddress,
		AdminStateUp:   f.adminStateUp,
		Tags:           f.tag,
	}
	// Weight 0 is meaningful — it takes a member out of rotation without removing
	// it — so it is sent whenever the flag was given, rather than being treated as
	// "unset" the way setIfNonZero would.
	if changed["weight"] {
		weight := f.weight
		opts.Weight = &weight
	}
	setIfNonZero(&opts.MonitorPort, f.monitorPort)
	if changed["enable-backup"] {
		backup := f.backup
		opts.Backup = &backup
	}

	m, err := pools.CreateMember(ctx, client, poolID, opts).Extract()
	if err != nil {
		return fmt.Errorf("adding member %q to pool %q: %w", name, poolRef, err)
	}
	fields, values := memberFields(m)
	return o.WriteSingle(w, fields, values)
}

// --- set -------------------------------------------------------------------

func newMemberSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &memberWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <pool> <member>",
		Short: "Update a pool member",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if fl.Changed("enable-backup") && fl.Changed("disable-backup") {
				return fmt.Errorf("--enable-backup and --disable-backup are mutually exclusive")
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
			return runMemberSet(ctx, client, o, args[0], args[1], f, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

func runMemberSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	poolRef, memberRef string, f *memberWriteFlags, changed changedSet, w io.Writer,
) error {
	opts := pools.UpdateMemberOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil

	assignString(changed, "name", f.name, &opts.Name, &touched)
	assignString(changed, "monitor-address", f.monitorAddress, &opts.MonitorAddress, &touched)
	assignInt(changed, "weight", f.weight, &opts.Weight, &touched)
	assignInt(changed, "monitor-port", f.monitorPort, &opts.MonitorPort, &touched)
	switch {
	case changed["enable-backup"]:
		assignBool(changed, "enable-backup", true, &opts.Backup, &touched)
	case changed["disable-backup"]:
		v := false
		opts.Backup = &v
		touched = true
	}
	switch {
	case f.noTag:
		opts.Tags = []string{}
		touched = true
	case changed["tag"]:
		opts.Tags = f.tag
		touched = true
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}

	poolID, err := resolvePoolID(ctx, client, poolRef)
	if err != nil {
		return err
	}
	memberID, err := resolveMemberID(ctx, client, poolID, memberRef)
	if err != nil {
		return err
	}
	m, err := pools.UpdateMember(ctx, client, poolID, memberID, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating member %q of pool %q: %w", memberRef, poolRef, err)
	}
	fields, values := memberFields(m)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newMemberDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <pool> <member> [<member>...]",
		Short: "Remove one or more members from a pool",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runMemberDelete(ctx, client, args[0], args[1:], cmd.OutOrStdout())
		},
	}
}

func runMemberDelete(ctx context.Context, client *gophercloud.ServiceClient,
	poolRef string, memberRefs []string, w io.Writer,
) error {
	poolID, err := resolvePoolID(ctx, client, poolRef)
	if err != nil {
		return err
	}
	return batchdelete.Each(memberRefs, func(ref string) error {
		memberID, rerr := resolveMemberID(ctx, client, poolID, ref)
		if rerr != nil {
			return rerr
		}
		if derr := pools.DeleteMember(ctx, client, poolID, memberID).ExtractErr(); derr != nil {
			return fmt.Errorf("removing member %q from pool %q: %w", ref, poolRef, derr)
		}
		if _, werr := fmt.Fprintf(w, "Requested removal of member %s from pool %s\n", ref, poolRef); werr != nil {
			return werr
		}
		return nil
	})
}
