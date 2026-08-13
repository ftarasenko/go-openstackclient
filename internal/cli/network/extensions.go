package network

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/extraroutes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/portforwarding"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/networkipavailabilities"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/rbacpolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/segments"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Four more neutron extensions: IP availability, RBAC policies, network
// segments and floating-IP port forwarding, plus the router route verbs that
// use the extraroute-atomic API.
//
// Flag names follow upstream OSC. UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.

func newIPAvailabilityCommand(a *auth.Options, o *output.Options) *cobra.Command {
	availability := &cobra.Command{Use: "availability", Short: "Show IP address availability"}
	availability.AddCommand(newIPAvailabilityListCommand(a, o), newIPAvailabilityShowCommand(a, o))
	cmd := &cobra.Command{Use: "ip", Short: "IP address commands"}
	cmd.AddCommand(availability)
	return cmd
}

func newIPAvailabilityListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var ipVersion int
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List IP address availability per network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runIPAvailabilityList(cmd.Context(), c, o, ipVersion, project, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&ipVersion, "ip-version", 0, "filter by IP version: 4 or 6")
	fl.StringVar(&project, "project", "", "filter by project ID")
	return cmd
}

func runIPAvailabilityList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ipVersion int, project string, w io.Writer,
) error {
	opts := networkipavailabilities.ListOpts{IPVersion: strconv.Itoa(ipVersion), ProjectID: project}
	if ipVersion == 0 {
		opts.IPVersion = ""
	}
	pages, err := networkipavailabilities.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing IP availability: %w", err)
	}
	all, err := networkipavailabilities.ExtractNetworkIPAvailabilities(pages)
	if err != nil {
		return fmt.Errorf("parsing the IP availability list: %w", err)
	}
	t := output.Table{
		Columns: []string{"Network ID", "Network Name", "Total IPs", "Used IPs"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, av := range all {
		t.Rows = append(t.Rows, []any{av.NetworkID, av.NetworkName, av.TotalIPs, av.UsedIPs})
	}
	return o.WriteList(w, t)
}

func newIPAvailabilityShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <network>",
		Short: "Show IP address availability for one network, per subnet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runIPAvailabilityShow(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runIPAvailabilityShow renders the per-subnet breakdown, which is the reason
// to look at one network rather than the list.
func runIPAvailabilityShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, w io.Writer,
) error {
	id, err := resolveNetworkID(ctx, client, ref)
	if err != nil {
		return err
	}
	av, err := networkipavailabilities.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing IP availability for network %s: %w", ref, err)
	}
	t := output.Table{
		Columns: []string{"Subnet ID", "Subnet Name", "CIDR", "IP Version", "Total IPs", "Used IPs"},
		Rows:    make([][]any, 0, len(av.SubnetIPAvailabilities)),
	}
	for _, sub := range av.SubnetIPAvailabilities {
		t.Rows = append(t.Rows, []any{sub.SubnetID, sub.SubnetName, sub.CIDR, sub.IPVersion, sub.TotalIPs, sub.UsedIPs})
	}
	return o.WriteList(w, t)
}

// --- network rbac -----------------------------------------------------------

func newRBACCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "rbac", Short: "Manage network RBAC policies"}
	cmd.AddCommand(
		newRBACListCommand(a, o),
		newRBACShowCommand(a, o),
		newRBACCreateCommand(a, o),
		newRBACSetCommand(a, o),
		newRBACDeleteCommand(a, o),
	)
	return cmd
}

func newRBACListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var action, objectType, targetProject string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List network RBAC policies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRBACList(cmd.Context(), c, o, action, objectType, targetProject, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&action, "action", "", "filter by action: access_as_external or access_as_shared")
	fl.StringVar(&objectType, "type", "", "filter by object type, e.g. network or qos_policy")
	fl.StringVar(&targetProject, "target-project", "", "filter by the project the policy grants access to")
	return cmd
}

func runRBACList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	action, objectType, targetProject string, w io.Writer,
) error {
	opts := rbacpolicies.ListOpts{
		Action:       rbacpolicies.PolicyAction(action),
		ObjectType:   objectType,
		TargetTenant: targetProject,
	}
	pages, err := rbacpolicies.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing network RBAC policies: %w", err)
	}
	all, err := rbacpolicies.ExtractRBACPolicies(pages)
	if err != nil {
		return fmt.Errorf("parsing the network RBAC policy list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Object Type", "Object ID", "Action", "Target Project"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.ID, p.ObjectType, p.ObjectID, p.Action, p.TargetTenant})
	}
	return o.WriteList(w, t)
}

func newRBACShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <rbac-policy>",
		Short: "Show a network RBAC policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRBACShow(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runRBACShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	p, err := rbacpolicies.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing network RBAC policy %s: %w", id, err)
	}
	return writeRBAC(o, w, p)
}

func writeRBAC(o *output.Options, w io.Writer, p *rbacpolicies.RBACPolicy) error {
	return o.WriteSingle(w,
		[]string{"id", "object_type", "object_id", "action", "target_project", "project_id"},
		[]any{p.ID, p.ObjectType, p.ObjectID, p.Action, p.TargetTenant, p.ProjectID})
}

// rbacAllProjects is neutron's wildcard target_tenant: an RBAC policy whose
// target is "*" grants the object to every project. Upstream OSC spells it
// --target-all-projects rather than making the operator know the wildcard
// (openstackclient/network/v2/network_rbac.py).
const rbacAllProjects = "*"

func newRBACCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var action, objectType, targetProject string
	var targetAllProjects bool
	cmd := &cobra.Command{
		Use:   "create <object-id>",
		Short: "Create a network RBAC policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if targetProject == "" && !targetAllProjects {
				return fmt.Errorf("one of --target-project or --target-all-projects is required")
			}
			if targetAllProjects {
				targetProject = rbacAllProjects
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRBACCreate(cmd.Context(), c, o, args[0], action, objectType, targetProject, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&action, "action", "", "access_as_external or access_as_shared")
	fl.StringVar(&objectType, "type", "", "object type, e.g. network or qos_policy")
	fl.StringVar(&targetProject, "target-project", "", "project to grant access to")
	fl.BoolVar(&targetAllProjects, "target-all-projects", false, "grant access to every project")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("type")
	// --target-project is no longer cobra-required because --target-all-projects
	// satisfies the same requirement; exactly one of the two is enforced in RunE.
	cmd.MarkFlagsMutuallyExclusive("target-project", "target-all-projects")
	return cmd
}

func runRBACCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	objectID, action, objectType, targetProject string, w io.Writer,
) error {
	p, err := rbacpolicies.Create(ctx, client, rbacpolicies.CreateOpts{
		Action:       rbacpolicies.PolicyAction(action),
		ObjectType:   objectType,
		ObjectID:     objectID,
		TargetTenant: targetProject,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating a network RBAC policy for %s: %w", objectID, err)
	}
	return writeRBAC(o, w, p)
}

func newRBACSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var targetProject string
	cmd := &cobra.Command{
		Use:   "set <rbac-policy>",
		Short: "Change the target project of a network RBAC policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRBACSet(cmd.Context(), c, o, args[0], targetProject, cmd.OutOrStdout())
		},
	}
	// The target project is the only mutable field: neutron's update schema has
	// nothing else in it.
	cmd.Flags().StringVar(&targetProject, "target-project", "", "project to grant access to")
	_ = cmd.MarkFlagRequired("target-project")
	return cmd
}

func runRBACSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, targetProject string, w io.Writer,
) error {
	p, err := rbacpolicies.Update(ctx, client, id, rbacpolicies.UpdateOpts{TargetTenant: targetProject}).Extract()
	if err != nil {
		return fmt.Errorf("updating network RBAC policy %s: %w", id, err)
	}
	return writeRBAC(o, w, p)
}

func newRBACDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <rbac-policy> [<rbac-policy> ...]",
		Short: "Delete network RBAC policies",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRBACDelete(cmd.Context(), c, args)
		},
	}
}

func runRBACDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := rbacpolicies.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting network RBAC policy %s: %w", id, err)
		}
		return nil
	})
}

// --- network segment --------------------------------------------------------

func newSegmentCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "segment", Short: "Manage network segments"}
	cmd.AddCommand(
		newSegmentListCommand(a, o),
		newSegmentShowCommand(a, o),
		newSegmentCreateCommand(a, o),
		newSegmentSetCommand(a, o),
		newSegmentDeleteCommand(a, o),
	)
	return cmd
}

func newSegmentListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var network, networkType, physicalNetwork string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List network segments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runSegmentList(cmd.Context(), c, o, network, networkType, physicalNetwork, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&network, "network", "", "filter by network (name or ID)")
	fl.StringVar(&networkType, "network-type", "", "filter by network type, e.g. vlan or vxlan")
	fl.StringVar(&physicalNetwork, "physical-network", "", "filter by physical network")
	return cmd
}

func runSegmentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	network, networkType, physicalNetwork string, w io.Writer,
) error {
	opts := segments.ListOpts{NetworkType: networkType, PhysicalNetwork: physicalNetwork}
	if network != "" {
		id, err := resolveNetworkID(ctx, client, network)
		if err != nil {
			return err
		}
		opts.NetworkID = id
	}
	pages, err := segments.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing network segments: %w", err)
	}
	all, err := segments.ExtractSegments(pages)
	if err != nil {
		return fmt.Errorf("parsing the network segment list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Name", "Network ID", "Network Type", "Segmentation ID"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, seg := range all {
		t.Rows = append(t.Rows, []any{seg.ID, seg.Name, seg.NetworkID, seg.NetworkType, seg.SegmentationID})
	}
	return o.WriteList(w, t)
}

func newSegmentShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <segment>",
		Short: "Show a network segment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runSegmentShow(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runSegmentShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	seg, err := segments.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing network segment %s: %w", id, err)
	}
	return writeSegment(o, w, seg)
}

func writeSegment(o *output.Options, w io.Writer, seg *segments.Segment) error {
	return o.WriteSingle(w,
		[]string{"id", "name", "description", "network_id", "network_type", "physical_network", "segmentation_id"},
		[]any{seg.ID, seg.Name, seg.Description, seg.NetworkID, seg.NetworkType, seg.PhysicalNetwork, seg.SegmentationID})
}

func newSegmentCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var network, networkType, physicalNetwork, description string
	var segmentationID int
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a network segment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runSegmentCreate(cmd.Context(), c, o, args[0], network, networkType,
				physicalNetwork, description, segmentationID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&network, "network", "", "network the segment belongs to (name or ID)")
	fl.StringVar(&networkType, "network-type", "", "network type, e.g. flat, vlan, vxlan or geneve")
	fl.StringVar(&physicalNetwork, "physical-network", "", "physical network name")
	fl.StringVar(&description, "description", "", "description of the segment")
	fl.IntVar(&segmentationID, "segment", 0, "segmentation ID, e.g. the VLAN tag")
	_ = cmd.MarkFlagRequired("network")
	_ = cmd.MarkFlagRequired("network-type")
	return cmd
}

func runSegmentCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, network, networkType, physicalNetwork, description string, segmentationID int, w io.Writer,
) error {
	networkID, err := resolveNetworkID(ctx, client, network)
	if err != nil {
		return err
	}
	seg, err := segments.Create(ctx, client, segments.CreateOpts{
		Name:            name,
		Description:     description,
		NetworkID:       networkID,
		NetworkType:     networkType,
		PhysicalNetwork: physicalNetwork,
		SegmentationID:  segmentationID,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating network segment %q: %w", name, err)
	}
	return writeSegment(o, w, seg)
}

func newSegmentSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, description string
	var segmentationID int
	cmd := &cobra.Command{
		Use:   "set <segment>",
		Short: "Set network segment properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runSegmentSet(cmd.Context(), c, o, args[0], name, description, segmentationID,
				fl.Changed("name"), fl.Changed("description"), fl.Changed("segment"), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new name")
	fl.StringVar(&description, "description", "", "new description")
	fl.IntVar(&segmentationID, "segment", 0, "new segmentation ID")
	return cmd
}

func runSegmentSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, name, description string, segmentationID int, nameSet, descSet, segSet bool, w io.Writer,
) error {
	opts := segments.UpdateOpts{}
	if nameSet {
		opts.Name = &name
	}
	if descSet {
		opts.Description = &description
	}
	if segSet {
		opts.SegmentationID = &segmentationID
	}
	seg, err := segments.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating network segment %s: %w", id, err)
	}
	return writeSegment(o, w, seg)
}

func newSegmentDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <segment> [<segment> ...]",
		Short: "Delete network segment(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runSegmentDelete(cmd.Context(), c, args)
		},
	}
}

func runSegmentDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := segments.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting network segment %s: %w", id, err)
		}
		return nil
	})
}

// --- floating ip port forwarding --------------------------------------------

func newPortForwardingCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "forwarding", Short: "Manage floating IP port forwarding"}
	cmd.AddCommand(
		newPortForwardingListCommand(a, o),
		newPortForwardingShowCommand(a, o),
		newPortForwardingCreateCommand(a, o),
		newPortForwardingSetCommand(a, o),
		newPortForwardingDeleteCommand(a, o),
	)
	parent := &cobra.Command{Use: "port", Short: "Floating IP port forwarding"}
	parent.AddCommand(cmd)
	return parent
}

func newPortForwardingListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <floating-ip>",
		Short: "List a floating IP's port forwardings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runPortForwardingList(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runPortForwardingList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	fipID string, w io.Writer,
) error {
	pages, err := portforwarding.List(client, portforwarding.ListOpts{}, fipID).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing port forwardings of floating IP %s: %w", fipID, err)
	}
	all, err := portforwarding.ExtractPortForwardings(pages)
	if err != nil {
		return fmt.Errorf("parsing the port forwarding list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Protocol", "External Port", "Internal Port", "Internal IP", "Internal Port ID"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, pf := range all {
		t.Rows = append(t.Rows, []any{pf.ID, pf.Protocol, pf.ExternalPort, pf.InternalPort,
			pf.InternalIPAddress, pf.InternalPortID})
	}
	return o.WriteList(w, t)
}

func newPortForwardingShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <floating-ip> <port-forwarding>",
		Short: "Show a floating IP port forwarding",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runPortForwardingShow(cmd.Context(), c, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runPortForwardingShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	fipID, id string, w io.Writer,
) error {
	pf, err := portforwarding.Get(ctx, client, fipID, id).Extract()
	if err != nil {
		return fmt.Errorf("showing port forwarding %s: %w", id, err)
	}
	return writePortForwarding(o, w, pf)
}

func writePortForwarding(o *output.Options, w io.Writer, pf *portforwarding.PortForwarding) error {
	return o.WriteSingle(w,
		[]string{"id", "protocol", "internal_port_id", "internal_ip_address", "internal_port",
			"internal_port_range", "external_port", "external_port_range", "description"},
		[]any{pf.ID, pf.Protocol, pf.InternalPortID, pf.InternalIPAddress, pf.InternalPort,
			pf.InternalPortRange, pf.ExternalPort, pf.ExternalPortRange, pf.Description})
}

type portForwardingFlags struct {
	port              string
	internalIP        string
	internalPort      int
	externalPort      int
	internalPortRange string
	externalPortRange string
	protocol          string
	description       string
}

// register wires the shared flags. defaultProtocol is "tcp" on create (neutron
// requires the field) and empty on set, where an unset flag must leave the
// stored protocol alone rather than silently rewrite it to tcp.
func (f *portForwardingFlags) register(cmd *cobra.Command, defaultProtocol string) {
	fl := cmd.Flags()
	fl.StringVar(&f.port, "port", "", "internal neutron port to forward to (name or ID)")
	fl.StringVar(&f.internalIP, "internal-ip-address", "", "internal IP address on that port")
	fl.IntVar(&f.internalPort, "internal-protocol-port", 0, "internal TCP/UDP port number")
	fl.IntVar(&f.externalPort, "external-protocol-port", 0, "external TCP/UDP port number")
	fl.StringVar(&f.internalPortRange, "internal-protocol-port-range", "",
		"internal port range as <first>:<last> (neutron 'port_forwarding_port_ranges' extension)")
	fl.StringVar(&f.externalPortRange, "external-protocol-port-range", "",
		"external port range as <first>:<last> (neutron 'port_forwarding_port_ranges' extension)")
	fl.StringVar(&f.protocol, "protocol", defaultProtocol, "protocol: tcp, udp, icmp, icmp6, sctp or dccp")
	fl.StringVar(&f.description, "description", "", "description of the forwarding")
}

func newPortForwardingCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portForwardingFlags{}
	cmd := &cobra.Command{
		Use:   "create <floating-ip>",
		Short: "Create a floating IP port forwarding",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runPortForwardingCreate(cmd.Context(), c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "tcp")
	_ = cmd.MarkFlagRequired("port")
	_ = cmd.MarkFlagRequired("internal-ip-address")
	return cmd
}

func runPortForwardingCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	fipID string, f *portForwardingFlags, w io.Writer,
) error {
	portID, err := resolvePortID(ctx, client, f.port)
	if err != nil {
		return err
	}
	// Neutron treats the single port and the port range as mutually exclusive,
	// so both are omitempty and only the pair the operator gave is sent.
	pf, err := portforwarding.Create(ctx, client, fipID, portforwarding.CreateOpts{
		Protocol:          f.protocol,
		InternalPortID:    portID,
		InternalIPAddress: f.internalIP,
		InternalPort:      f.internalPort,
		ExternalPort:      f.externalPort,
		InternalPortRange: f.internalPortRange,
		ExternalPortRange: f.externalPortRange,
		Description:       f.description,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating a port forwarding on floating IP %s: %w", fipID, err)
	}
	return writePortForwarding(o, w, pf)
}

func newPortForwardingSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portForwardingFlags{}
	cmd := &cobra.Command{
		Use:   "set <floating-ip> <port-forwarding>",
		Short: "Update a floating IP port forwarding",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runPortForwardingSet(cmd.Context(), c, o, args[0], args[1], f,
				cmd.Flags().Changed("description"), cmd.OutOrStdout())
		},
	}
	// Unlike create, nothing is required: neutron patches only what is sent.
	f.register(cmd, "")
	return cmd
}

func runPortForwardingSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	fipID, id string, f *portForwardingFlags, descSet bool, w io.Writer,
) error {
	opts := portforwarding.UpdateOpts{
		InternalIPAddress: f.internalIP,
		InternalPort:      f.internalPort,
		ExternalPort:      f.externalPort,
		InternalPortRange: f.internalPortRange,
		ExternalPortRange: f.externalPortRange,
		Protocol:          f.protocol,
	}
	if f.port != "" {
		portID, err := resolvePortID(ctx, client, f.port)
		if err != nil {
			return err
		}
		opts.InternalPortID = portID
	}
	if descSet {
		opts.Description = &f.description
	}
	pf, err := portforwarding.Update(ctx, client, fipID, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating port forwarding %s: %w", id, err)
	}
	return writePortForwarding(o, w, pf)
}

func newPortForwardingDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <floating-ip> <port-forwarding> [<port-forwarding> ...]",
		Short: "Delete floating IP port forwarding(s)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runPortForwardingDelete(cmd.Context(), c, args[0], args[1:])
		},
	}
}

func runPortForwardingDelete(ctx context.Context, client *gophercloud.ServiceClient, fipID string, ids []string) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := portforwarding.Delete(ctx, client, fipID, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting port forwarding %s: %w", id, err)
		}
		return nil
	})
}

// --- router add/remove route ------------------------------------------------

// newRouterRouteCommands builds "router add route" and "router remove route".
// They use neutron's extraroute-atomic actions rather than a plain router
// update: the update replaces the whole routes list, so two concurrent callers
// would silently drop each other's routes.
func newRouterAddRouteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var routeSpecs []string
	cmd := &cobra.Command{
		Use:   "route <router>",
		Short: "Add static route(s) to a router",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRouterRoute(cmd.Context(), c, o, args[0], routeSpecs, true, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&routeSpecs, "route", nil,
		"route as destination=<cidr>,gateway=<ip> (repeatable)")
	_ = cmd.MarkFlagRequired("route")
	return cmd
}

func newRouterRemoveRouteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var routeSpecs []string
	cmd := &cobra.Command{
		Use:   "route <router>",
		Short: "Remove static route(s) from a router",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runRouterRoute(cmd.Context(), c, o, args[0], routeSpecs, false, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&routeSpecs, "route", nil,
		"route as destination=<cidr>,gateway=<ip> (repeatable)")
	_ = cmd.MarkFlagRequired("route")
	return cmd
}

func runRouterRoute(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	routerRef string, specs []string, add bool, w io.Writer,
) error {
	routerID, err := resolveRouterID(ctx, client, routerRef)
	if err != nil {
		return err
	}
	parsed, err := parseRouterRoutes(specs)
	if err != nil {
		return err
	}
	opts := extraroutes.Opts{Routes: &parsed}
	var r *routers.Router
	if add {
		r, err = extraroutes.Add(ctx, client, routerID, opts).Extract()
	} else {
		r, err = extraroutes.Remove(ctx, client, routerID, opts).Extract()
	}
	if err != nil {
		return fmt.Errorf("updating the routes of router %s: %w", routerRef, err)
	}
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
}

func parseRouterRoutes(specs []string) ([]routers.Route, error) {
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
			return nil, fmt.Errorf("--route %q needs both destination= and gateway=", spec)
		}
		out = append(out, route)
	}
	return out, nil
}
