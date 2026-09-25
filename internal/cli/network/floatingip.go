package network

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
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
	cmd.AddCommand(newPortForwardingCommand(a, o))
	return cmd
}

// floatingIPExt is a FloatingIP plus the extension attributes gophercloud's
// struct does not model: qos_policy_id (qos-fip) and dns_name/dns_domain
// (dns-integration). The attributes arrive through an exported flat embed so
// ExtractInto decodes them alongside FloatingIP's own UnmarshalJSON (see
// portExt).
type floatingIPExt struct {
	floatingips.FloatingIP
	FloatingIPAttrsExt
}

// FloatingIPAttrsExt carries the floating-IP extension attributes.
type FloatingIPAttrsExt struct {
	QoSPolicyID string `json:"qos_policy_id"`
	DNSName     string `json:"dns_name"`
	DNSDomain   string `json:"dns_domain"`
}

func floatingIPShowFields(f *floatingIPExt) ([]string, []any) {
	fields := []string{
		"id", "floating_ip_address", "floating_network_id", "fixed_ip_address",
		"port_id", "router_id", "status", "description", "project_id",
		"qos_policy_id", "dns_name", "dns_domain", "tags",
		"created_at", "updated_at",
	}
	values := []any{
		f.ID, f.FloatingIP.FloatingIP, f.FloatingNetworkID, f.FixedIP,
		f.PortID, f.RouterID, f.Status, f.Description, f.ProjectID,
		f.QoSPolicyID, f.DNSName, f.DNSDomain, f.Tags,
		f.CreatedAt, f.UpdatedAt,
	}
	return fields, values
}

func getFloatingIP(ctx context.Context, client *gophercloud.ServiceClient, id string) (*floatingIPExt, error) {
	var f floatingIPExt
	if err := floatingips.Get(ctx, client, id).ExtractInto(&f); err != nil {
		return nil, err
	}
	return &f, nil
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

// floatingIPListFlags mirrors upstream ListFloatingIP
// (network/v2/floating_ip.py): --network, --port and --router are repeatable
// and neutron ORs the repeated values; the rest are exact-match filters.
type floatingIPListFlags struct {
	networks          []string
	ports             []string
	routers           []string
	fixedIPAddress    string
	floatingIPAddress string
	status            string
	project           string
	projectDomain     string
	long              bool
	tagFilterFlags
}

func newFloatingIPListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &floatingIPListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List floating IPs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.status != "" && f.status != "ACTIVE" && f.status != "DOWN" {
				return fmt.Errorf("--status must be ACTIVE or DOWN, got %q", f.status)
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runFloatingIPList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.networks, "network", nil, "list only floating IPs allocated from this network (name or ID, repeatable)")
	fl.StringArrayVar(&f.ports, "port", nil, "list only floating IPs associated with this port (name or ID, repeatable)")
	fl.StringArrayVar(&f.routers, "router", nil, "list only floating IPs attached to this router (name or ID, repeatable)")
	fl.StringVar(&f.fixedIPAddress, "fixed-ip-address", "", "list only floating IPs bound to this fixed IP address")
	fl.StringVar(&f.floatingIPAddress, "floating-ip-address", "", "list only this floating IP address")
	fl.StringVar(&f.status, "status", "", "list only floating IPs in this status (ACTIVE, DOWN)")
	fl.StringVar(&f.project, flagProject, "", "list only floating IPs owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	bindTagFilterFlags(fl, &f.tagFilterFlags, "floating IPs")
	return cmd
}

// floatingIPListOpts adds the repeated filters ListOpts cannot carry.
type floatingIPListOpts struct {
	floatingips.ListOpts
	extra url.Values
}

func (opts floatingIPListOpts) ToFloatingIPListQuery() (string, error) {
	q, err := opts.ListOpts.ToFloatingIPListQuery()
	return withQueryValues(q, err, opts.extra)
}

func runFloatingIPList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *floatingIPListFlags, projectID string, w io.Writer) error {
	opts := floatingIPListOpts{
		ListOpts: floatingips.ListOpts{
			FixedIP:    f.fixedIPAddress,
			FloatingIP: f.floatingIPAddress,
			Status:     f.status,
			ProjectID:  projectID,
		},
		extra: url.Values{},
	}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
	for _, filter := range []struct {
		key      string
		refs     []string
		resolver func(context.Context, *gophercloud.ServiceClient, string) (string, error)
	}{
		{"floating_network_id", f.networks, resolveNetworkID},
		{"port_id", f.ports, resolvePortID},
		{"router_id", f.routers, resolveRouterID},
	} {
		ids, err := resolveEach(ctx, client, filter.refs, filter.resolver)
		if err != nil {
			return err
		}
		opts.extra[filter.key] = ids
	}
	pages, err := floatingips.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing floating IPs: %w", err)
	}
	var all []floatingIPExt
	if err := floatingips.ExtractFloatingIPsInto(pages, &all); err != nil {
		return fmt.Errorf("parsing floating IP list: %w", err)
	}
	return o.WriteList(w, floatingIPListTable(all, f.long))
}

// floatingIPListTable renders upstream's columns: ID, addresses, port, floating
// network and project by default; --long adds router, status, description,
// tags and the DNS pair.
func floatingIPListTable(all []floatingIPExt, long bool) output.Table {
	cols := []string{"ID", "Floating IP Address", "Fixed IP Address", "Port", "Floating Network", "Project"}
	if long {
		cols = append(cols, "Router", "Status", "Description", "Tags", "DNS Name", "DNS Domain")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, f := range all {
		row := []any{f.ID, f.FloatingIP.FloatingIP, f.FixedIP, f.PortID, f.FloatingNetworkID, f.ProjectID}
		if long {
			row = append(row, f.RouterID, f.Status, f.Description, f.Tags, f.DNSName, f.DNSDomain)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
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
	f, err := getFloatingIP(ctx, client, id)
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
	qosPolicy         string
	dnsDomain         string
	dnsName           string
	project           string
	projectDomain     string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	extraProperty []string
	tagWriteFlags
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
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runFloatingIPCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.floatingIPAddress, "floating-ip-address", "", "specific floating IP address to allocate")
	fl.StringVar(&f.subnet, "subnet", "", "subnet on which to allocate the floating IP (name or ID)")
	fl.StringVar(&f.description, flagDescription, "", "description for the floating IP")
	fl.StringVar(&f.port, "port", "", "port to associate the floating IP with at creation (name or ID)")
	fl.StringVar(&f.fixedIPAddr, "fixed-ip-address", "", "fixed IP of the associated port to bind the floating IP to")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy to attach to the floating IP (name or ID)")
	fl.StringVar(&f.dnsDomain, flagDNSDomain, "", "DNS domain for the floating IP (requires the dns-integration extension)")
	fl.StringVar(&f.dnsName, flagDNSName, "", "DNS name for the floating IP (requires the dns-integration extension)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagCreateFlags(cmd, &f.tagWriteFlags, "floating IP")
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
		ProjectID:         f.projectID,
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
	var attrs map[string]any
	if f.qosPolicy != "" {
		qosID, err := resolveQoSPolicyID(ctx, client, f.qosPolicy)
		if err != nil {
			return err
		}
		attrs = mergeAttrs(attrs, map[string]any{"qos_policy_id": qosID})
	}
	if f.dnsDomain != "" {
		attrs = mergeAttrs(attrs, map[string]any{"dns_domain": f.dnsDomain})
	}
	if f.dnsName != "" {
		attrs = mergeAttrs(attrs, map[string]any{"dns_name": f.dnsName})
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)

	var fip floatingIPExt
	if err := floatingips.Create(ctx, client, withFloatingIPCreateAttrs(opts, attrs)).ExtractInto(&fip); err != nil {
		return explainMissingExtension(ctx, client, fmt.Errorf("creating floating IP: %w", err), attrs)
	}
	// Tags cannot ride on the create; upstream sets them afterwards too.
	if fip.Tags, err = applyTagsForSet(ctx, client, tagResourceFloatingIPs, fip.ID, fip.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := floatingIPShowFields(&fip)
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
	return batchdelete.Each(addrs, func(addrOrID string) error {
		id, err := resolveFloatingIPID(ctx, client, addrOrID)
		if err != nil {
			return err
		}
		if err := floatingips.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting floating IP %s: %w", addrOrID, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted floating IP %s\n", addrOrID); err != nil {
			return err
		}
		return nil
	})
}

type floatingIPSetFlags struct {
	port          string
	fixedIPAddr   string
	description   string
	qosPolicy     string
	noQoSPolicy   bool
	extraProperty []string
	tagWriteFlags
}

func newFloatingIPSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &floatingIPSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <floating-ip>",
		Short: "Set floating IP properties",
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
			return runFloatingIPSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.port, "port", "", "port to associate with the floating IP (name or ID)")
	fl.StringVar(&f.fixedIPAddr, "fixed-ip-address", "", "fixed IP of the port to associate")
	fl.StringVar(&f.description, flagDescription, "", "new description for the floating IP")
	fl.StringVar(&f.qosPolicy, flagQoSPolicy, "", "QoS policy to attach to the floating IP (name or ID)")
	fl.BoolVar(&f.noQoSPolicy, flagNoQoSPolicy, false, "detach the floating IP's QoS policy")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagSetFlags(fl, &f.tagWriteFlags, "floating IP")
	cmd.MarkFlagsMutuallyExclusive(flagQoSPolicy, flagNoQoSPolicy)
	return cmd
}

func runFloatingIPSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, addrOrID string, f *floatingIPSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolveFloatingIPID(ctx, client, addrOrID)
	if err != nil {
		return err
	}
	opts := floatingips.UpdateOpts{FixedIP: f.fixedIPAddr}
	changed := f.fixedIPAddr != ""
	if f.port != "" {
		portID, err := resolvePortID(ctx, client, f.port)
		if err != nil {
			return err
		}
		opts.PortID = &portID
		changed = true
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
		changed = true
	}
	var attrs map[string]any
	switch {
	case f.noQoSPolicy:
		attrs = map[string]any{"qos_policy_id": nil}
	case f.qosPolicy != "":
		qosID, err := resolveQoSPolicyID(ctx, client, f.qosPolicy)
		if err != nil {
			return err
		}
		attrs = map[string]any{"qos_policy_id": qosID}
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	if len(attrs) > 0 {
		changed = true
	}
	if !changed && !f.given() {
		return fmt.Errorf("floating ip set requires at least one attribute flag")
	}
	return updateFloatingIP(ctx, client, o, addrOrID, id, opts, attrs, changed, applyTagsForSet, &f.tagWriteFlags, w)
}

// updateFloatingIP is the shared tail of set and unset: PUT the attributes when
// any were given (upstream skips the update when only tags change), then apply
// the tag change, then render what the floating IP now looks like.
func updateFloatingIP(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, id string,
	opts floatingips.UpdateOpts, attrs map[string]any, changed bool,
	applyTags func(context.Context, *gophercloud.ServiceClient, string, string, []string, *tagWriteFlags) ([]string, error),
	tags *tagWriteFlags, w io.Writer,
) error {
	var (
		fip *floatingIPExt
		err error
	)
	if changed {
		fip = &floatingIPExt{}
		if err := floatingips.Update(ctx, client, id, withFloatingIPUpdateAttrs(opts, attrs)).ExtractInto(fip); err != nil {
			return explainMissingExtension(ctx, client, fmt.Errorf("updating floating IP %s: %w", ref, err), attrs)
		}
	} else if fip, err = getFloatingIP(ctx, client, id); err != nil {
		return fmt.Errorf("getting floating IP %s: %w", ref, err)
	}
	if fip.Tags, err = applyTags(ctx, client, tagResourceFloatingIPs, id, fip.Tags, tags); err != nil {
		return err
	}
	fields, values := floatingIPShowFields(fip)
	return o.WriteSingle(w, fields, values)
}

type floatingIPUnsetFlags struct {
	port          bool
	qosPolicy     bool
	extraProperty []string
	tagWriteFlags
}

func newFloatingIPUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &floatingIPUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <floating-ip>",
		Short: "Unset floating IP properties",
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
			return runFloatingIPUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.port, "port", false, "disassociate the floating IP from its port")
	fl.BoolVar(&f.qosPolicy, flagQoSPolicy, false, "detach the floating IP's QoS policy")
	// Upstream's UnsetFloatingIP inherits the create/set --extra-property parser
	// and so sends the given value; koc clears the named attribute (null), as
	// every other upstream unset verb does.
	bindExtraPropertyUnsetFlag(fl, &f.extraProperty)
	bindTagUnsetFlags(cmd, &f.tagWriteFlags, "floating IP")
	return cmd
}

func runFloatingIPUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, addrOrID string, f *floatingIPUnsetFlags, w io.Writer) error {
	id, err := resolveFloatingIPID(ctx, client, addrOrID)
	if err != nil {
		return err
	}
	opts := floatingips.UpdateOpts{}
	var attrs map[string]any
	if f.port {
		// A nil PortID marshals to null via ToFloatingIPUpdateMap, disassociating.
		empty := ""
		opts.PortID = &empty
	}
	if f.qosPolicy {
		attrs = map[string]any{"qos_policy_id": nil}
	}
	extra, err := parseExtraProperties(f.extraProperty, true)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	changed := f.port || len(attrs) > 0
	if !changed && !f.given() {
		return fmt.Errorf("floating ip unset requires at least one attribute flag")
	}
	return updateFloatingIP(ctx, client, o, addrOrID, id, opts, attrs, changed, applyTagsForUnset, &f.tagWriteFlags, w)
}
