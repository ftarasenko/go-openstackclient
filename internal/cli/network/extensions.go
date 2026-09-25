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
	"github.com/gophercloud/gophercloud/v2/pagination"
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
	var project, projectDomain string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List IP address availability per network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			c, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, project, projectDomain)
			if err != nil {
				return err
			}
			return runIPAvailabilityList(ctx, c, o, ipVersion, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&ipVersion, "ip-version", 0, "filter by IP version: 4 or 6")
	fl.StringVar(&project, flagProject, "", "list only networks owned by this project (name or ID)")
	fl.StringVar(&projectDomain, flagProjectDomain, "", projectDomainHelp)
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
	details, err := ipAvailabilityDetails(pages)
	if err != nil {
		return fmt.Errorf("parsing the IP availability list: %w", err)
	}
	t := output.Table{
		Columns: []string{
			"Network ID", "Network Name", "Total IPs", "Used IPs",
			"Total IPs in Subnet", "Total IPs in Allocation Pool",
			"Used IPs in Subnet", "Used IPs in Allocation Pool",
		},
		Rows: make([][]any, 0, len(all)),
	}
	for i, av := range all {
		row := []any{av.NetworkID, av.NetworkName, av.TotalIPs, av.UsedIPs}
		for _, field := range ipAvailabilityDetailFields {
			var v any = ""
			if i < len(details) && details[i] != nil {
				if d, ok := details[i][field]; ok {
					v = d
				}
			}
			row = append(row, v)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// ipAvailabilityDetailFields are the four keys of ip_availability_details,
// upstream's last four list columns (ip_availability.py _DETAIL_FIELDS). The
// attribute exists only with the network-ip-availability-details extension, so
// a cloud without it renders the columns empty, as upstream does.
var ipAvailabilityDetailFields = []string{
	"total_ips_in_subnet", "total_ips_in_allocation_pool",
	"used_ips_in_subnet", "used_ips_in_allocation_pool",
}

// ipAvailabilityDetails reads ip_availability_details off each listed network,
// index-aligned with ExtractNetworkIPAvailabilities. It is decoded separately
// because NetworkIPAvailability has its own UnmarshalJSON (for total_ips as a
// big integer), which an embedding struct would inherit and so lose the field.
func ipAvailabilityDetails(pages pagination.Page) ([]map[string]any, error) {
	var rows []struct {
		Details map[string]any `json:"ip_availability_details"`
	}
	page, ok := pages.(networkipavailabilities.NetworkIPAvailabilityPage)
	if !ok {
		return nil, fmt.Errorf("unexpected page type %T", pages)
	}
	if err := page.ExtractIntoSlicePtr(&rows, "network_ip_availabilities"); err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = r.Details
	}
	return out, nil
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
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List network RBAC policies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			c, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			// Upstream resolves the target project without a domain and passes
			// the "*" wildcard through untouched.
			targetID, err := resolveRBACTargetProject(ctx, session, targetProject, "")
			if err != nil {
				return err
			}
			return runRBACList(ctx, c, o, action, objectType, targetID, long, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&action, "action", "", "filter by action: access_as_external or access_as_shared")
	fl.StringVar(&objectType, "type", "", "filter by object type, e.g. network or qos_policy")
	fl.StringVar(&targetProject, flagTargetProject, "", "filter by the project the policy grants access to (name or ID)")
	fl.BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

// resolveRBACTargetProject resolves a --target-project, leaving neutron's "*"
// wildcard alone: it names every project, not a keystone project called "*".
func resolveRBACTargetProject(ctx context.Context, session *auth.Client, ref, domainRef string) (string, error) {
	if ref == rbacAllProjects {
		return ref, nil
	}
	return resolveProjectRef(ctx, session, ref, domainRef)
}

// runRBACList renders upstream's columns: ID, object type and object ID, with
// the action under --long. koc also shows the target project under --long,
// which upstream's list never does.
func runRBACList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	action, objectType, targetProject string, long bool, w io.Writer,
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
	cols := []string{"ID", "Object Type", "Object ID"}
	if long {
		cols = append(cols, "Action", "Target Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		row := []any{p.ID, p.ObjectType, p.ObjectID}
		if long {
			row = append(row, p.Action, p.TargetTenant)
		}
		t.Rows = append(t.Rows, row)
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

// flagTargetProjectDomain disambiguates --target-project the way
// --project-domain does --project.
const flagTargetProjectDomain = "target-project-domain"

type rbacCreateFlags struct {
	action              string
	objectType          string
	targetProject       string
	targetProjectDomain string
	targetAllProjects   bool
	project             string
	projectDomain       string
	// projectID is the owner (--project) resolved by RunE; targetProject is
	// likewise replaced by its resolved ID (or "*").
	projectID     string
	extraProperty []string
}

func newRBACCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &rbacCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <object-id>",
		Short: "Create a network RBAC policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.targetProject == "" && !f.targetAllProjects {
				return fmt.Errorf("one of --target-project or --target-all-projects is required")
			}
			ctx := cmd.Context()
			c, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.targetAllProjects {
				f.targetProject = rbacAllProjects
			} else if f.targetProject, err = resolveProjectRef(ctx, session, f.targetProject, f.targetProjectDomain); err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runRBACCreate(ctx, c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.action, "action", "", "access_as_external or access_as_shared")
	fl.StringVar(&f.objectType, "type", "", "object type, e.g. network or qos_policy")
	fl.StringVar(&f.targetProject, flagTargetProject, "", "project to grant access to (name or ID)")
	fl.StringVar(&f.targetProjectDomain, flagTargetProjectDomain, "",
		"domain owning --target-project (name or ID), to disambiguate a project name")
	fl.BoolVar(&f.targetAllProjects, flagTargetAllProjects, false, "grant access to every project")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("type")
	// --target-project is no longer cobra-required because --target-all-projects
	// satisfies the same requirement; exactly one of the two is enforced in RunE.
	cmd.MarkFlagsMutuallyExclusive(flagTargetProject, flagTargetAllProjects)
	return cmd
}

func runRBACCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	objectID string, f *rbacCreateFlags, w io.Writer,
) error {
	opts := rbacpolicies.CreateOpts{
		Action:       rbacpolicies.PolicyAction(f.action),
		ObjectType:   f.objectType,
		ObjectID:     objectID,
		TargetTenant: f.targetProject,
	}
	// CreateOpts has no owner field: project_id rides as an extra attribute.
	var attrs map[string]any
	if f.projectID != "" {
		attrs = map[string]any{"project_id": f.projectID}
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	p, err := rbacpolicies.Create(ctx, client, withRbacCreateAttrs(opts, attrs)).Extract()
	if err != nil {
		return fmt.Errorf("creating a network RBAC policy for %s: %w", objectID, err)
	}
	return writeRBAC(o, w, p)
}

type rbacSetFlags struct {
	targetProject       string
	targetProjectDomain string
	extraProperty       []string
}

func newRBACSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &rbacSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <rbac-policy>",
		Short: "Change the target project of a network RBAC policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.targetProject == "" && len(f.extraProperty) == 0 {
				return fmt.Errorf("network rbac set requires --target-project or --extra-property")
			}
			ctx := cmd.Context()
			c, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.targetProject, err = resolveRBACTargetProject(ctx, session, f.targetProject, f.targetProjectDomain); err != nil {
				return err
			}
			return runRBACSet(ctx, c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	// The target project is the only mutable field in neutron's update schema;
	// --extra-property is upstream's escape hatch for anything a newer
	// neutron adds.
	fl := cmd.Flags()
	fl.StringVar(&f.targetProject, flagTargetProject, "", "project to grant access to (name or ID)")
	fl.StringVar(&f.targetProjectDomain, flagTargetProjectDomain, "",
		"domain owning --target-project (name or ID), to disambiguate a project name")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	return cmd
}

// runRBACSet sends target_tenant (already resolved) plus any extra attributes.
// UpdateOpts tags target_tenant required, so an extra-property-only update
// builds its body without it.
func runRBACSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *rbacSetFlags, w io.Writer,
) error {
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	var opts rbacpolicies.UpdateOptsBuilder
	if f.targetProject != "" {
		opts = withRbacUpdateAttrs(rbacpolicies.UpdateOpts{TargetTenant: f.targetProject}, extra)
	} else {
		opts = rbacUpdateExt{bodyExt: bodyExt{
			build: func() (map[string]any, error) { return map[string]any{"rbac_policy": map[string]any{}}, nil },
			key:   "rbac_policy",
			extra: extra,
		}}
	}
	p, err := rbacpolicies.Update(ctx, client, id, opts).Extract()
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
	var long bool
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
			return runSegmentList(cmd.Context(), c, o, network, networkType, physicalNetwork, long, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&network, "network", "", "filter by network (name or ID)")
	fl.StringVar(&networkType, flagNetworkType, "", "filter by network type, e.g. vlan or vxlan")
	fl.StringVar(&physicalNetwork, "physical-network", "", "filter by physical network")
	fl.BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

// runSegmentList renders upstream's columns; --long adds the physical network.
// --network-type and --physical-network are koc-only filters.
func runSegmentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	network, networkType, physicalNetwork string, long bool, w io.Writer,
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
	cols := []string{"ID", "Name", "Network", "Network Type", "Segment"}
	if long {
		cols = append(cols, "Physical Network")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, seg := range all {
		row := []any{seg.ID, seg.Name, seg.NetworkID, seg.NetworkType, seg.SegmentationID}
		if long {
			row = append(row, seg.PhysicalNetwork)
		}
		t.Rows = append(t.Rows, row)
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

type segmentCreateFlags struct {
	network         string
	networkType     string
	physicalNetwork string
	description     string
	segmentationID  int
	extraProperty   []string
}

func newSegmentCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &segmentCreateFlags{}
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
			return runSegmentCreate(cmd.Context(), c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.network, "network", "", "network the segment belongs to (name or ID)")
	fl.StringVar(&f.networkType, flagNetworkType, "", "network type, e.g. flat, vlan, vxlan or geneve")
	fl.StringVar(&f.physicalNetwork, "physical-network", "", "physical network name")
	fl.StringVar(&f.description, "description", "", "description of the segment")
	fl.IntVar(&f.segmentationID, "segment", 0, "segmentation ID, e.g. the VLAN tag")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	_ = cmd.MarkFlagRequired("network")
	_ = cmd.MarkFlagRequired(flagNetworkType)
	return cmd
}

func runSegmentCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *segmentCreateFlags, w io.Writer,
) error {
	networkID, err := resolveNetworkID(ctx, client, f.network)
	if err != nil {
		return err
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	seg, err := segments.Create(ctx, client, withSegmentCreateAttrs(segments.CreateOpts{
		Name:            name,
		Description:     f.description,
		NetworkID:       networkID,
		NetworkType:     f.networkType,
		PhysicalNetwork: f.physicalNetwork,
		SegmentationID:  f.segmentationID,
	}, extra)).Extract()
	if err != nil {
		return fmt.Errorf("creating network segment %q: %w", name, err)
	}
	return writeSegment(o, w, seg)
}

type segmentSetFlags struct {
	name           string
	description    string
	segmentationID int
	extraProperty  []string

	// Which of the three were given: each field is a pointer in UpdateOpts, so
	// an empty name or a zero segmentation ID is still a real update.
	nameSet bool
	descSet bool
	segSet  bool
}

func newSegmentSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &segmentSetFlags{}
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
			f.nameSet, f.descSet, f.segSet = fl.Changed("name"), fl.Changed("description"), fl.Changed("segment")
			return runSegmentSet(cmd.Context(), c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name")
	fl.StringVar(&f.description, "description", "", "new description")
	fl.IntVar(&f.segmentationID, "segment", 0, "new segmentation ID")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	return cmd
}

func runSegmentSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *segmentSetFlags, w io.Writer,
) error {
	opts := segments.UpdateOpts{}
	if f.nameSet {
		opts.Name = &f.name
	}
	if f.descSet {
		opts.Description = &f.description
	}
	if f.segSet {
		opts.SegmentationID = &f.segmentationID
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	seg, err := segments.Update(ctx, client, id, withSegmentUpdateAttrs(opts, extra)).Extract()
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

// portForwardingListFlags are upstream's list filters: the internal port
// (name or ID), the external port — a single number, or <first>:<last> for a
// range — and the protocol.
type portForwardingListFlags struct {
	port         string
	externalPort string
	protocol     string
}

func newPortForwardingListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portForwardingListFlags{}
	cmd := &cobra.Command{
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
			return runPortForwardingList(cmd.Context(), c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.port, "port", "", "list only forwardings to this internal port (name or ID)")
	fl.StringVar(&f.externalPort, "external-protocol-port", "",
		"list only forwardings on this external port number, or <first>:<last> range")
	fl.StringVar(&f.protocol, "protocol", "", "list only forwardings using this protocol")
	return cmd
}

func runPortForwardingList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	fipID string, f *portForwardingListFlags, w io.Writer,
) error {
	opts := portforwarding.ListOpts{Protocol: f.protocol}
	if f.port != "" {
		portID, err := resolvePortID(ctx, client, f.port)
		if err != nil {
			return err
		}
		opts.InternalPortID = portID
	}
	// Upstream sends a range as external_port_range and a single port as
	// external_port (an integer).
	switch {
	case strings.Contains(f.externalPort, ":"):
		opts.ExternalPortRange = f.externalPort
	case f.externalPort != "":
		n, err := strconv.Atoi(f.externalPort)
		if err != nil {
			return fmt.Errorf("--external-protocol-port %q is not a port number or <first>:<last> range", f.externalPort)
		}
		opts.ExternalPort = strconv.Itoa(n)
	}
	pages, err := portforwarding.List(client, opts, fipID).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing port forwardings of floating IP %s: %w", fipID, err)
	}
	all, err := portforwarding.ExtractPortForwardings(pages)
	if err != nil {
		return fmt.Errorf("parsing the port forwarding list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Internal Port ID", "Internal IP Address", "Internal Port", "Internal Port Range",
			"External Port", "External Port Range", "Protocol", "Description"},
		Rows: make([][]any, 0, len(all)),
	}
	for _, pf := range all {
		t.Rows = append(t.Rows, []any{pf.ID, pf.InternalPortID, pf.InternalIPAddress, pf.InternalPort,
			pf.InternalPortRange, pf.ExternalPort, pf.ExternalPortRange, pf.Protocol, pf.Description})
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
	extraProperty     []string

	// descSet records whether --description was given: clearing a description
	// is a real update, so an empty value cannot stand in for "not given".
	descSet bool
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
	bindExtraPropertyFlag(fl, &f.extraProperty)
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
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	// Neutron treats the single port and the port range as mutually exclusive,
	// so both are omitempty and only the pair the operator gave is sent.
	pf, err := portforwarding.Create(ctx, client, fipID, withPortForwardingCreateAttrs(portforwarding.CreateOpts{
		Protocol:          f.protocol,
		InternalPortID:    portID,
		InternalIPAddress: f.internalIP,
		InternalPort:      f.internalPort,
		ExternalPort:      f.externalPort,
		InternalPortRange: f.internalPortRange,
		ExternalPortRange: f.externalPortRange,
		Description:       f.description,
	}, extra)).Extract()
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
			f.descSet = cmd.Flags().Changed("description")
			return runPortForwardingSet(cmd.Context(), c, o, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	// Unlike create, nothing is required: neutron patches only what is sent.
	f.register(cmd, "")
	return cmd
}

func runPortForwardingSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	fipID, id string, f *portForwardingFlags, w io.Writer,
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
	if f.descSet {
		opts.Description = &f.description
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	pf, err := portforwarding.Update(ctx, client, fipID, id, withPortForwardingUpdateAttrs(opts, extra)).Extract()
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
