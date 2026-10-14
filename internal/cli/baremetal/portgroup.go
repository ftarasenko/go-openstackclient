package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Port groups model bonded NICs (LACP, vPC): member ports share the group's
// address, and neutron binds the VIF to the group. gophercloud has no
// portgroups package, so these verbs use the raw helpers in raw.go
// (/v1/portgroups, API 1.23; mode and properties 1.26).
//
// Flag names follow python-ironicclient (`openstack baremetal port group`).
// UNVERIFIED against KeyStack (docs.keystack.ru unreachable, HTTP 403).

var (
	featurePortGroupPhysnet  = ironicFeature{"baremetal port group --physical-network", "1.102", releaseGazpacho}
	featurePortGroupCategory = ironicFeature{"baremetal port group --category", "1.103", releaseGazpacho}
)

// portGroupDetailFields are python-ironicclient's PORTGROUP_DETAILED_RESOURCE
// plus "ports", the member port UUIDs, which koc adds: upstream shows only a
// link to them. portGroupFields are the short list's columns.
var (
	portGroupDetailFields = []string{
		"uuid", "address", "created_at", "extra", "standalone_ports_supported", "node_uuid",
		"name", "updated_at", "internal_info", "mode", "properties", "physical_network",
		"category", "ports",
	}
	portGroupFields  = []string{"uuid", "address", "name"}
	portGroupColumns = knownColumns(portGroupDetailFields)
)

// newPortGroupCommand builds "baremetal port group ...". It is attached under
// "baremetal port", as upstream's two-word noun is.
func newPortGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "group",
		Short:   "Manage baremetal port groups (bonded NICs)",
		Aliases: []string{"groups"},
	}
	cmd.AddCommand(
		newPortGroupListCommand(a, o),
		newPortGroupShowCommand(a, o),
		newPortGroupCreateCommand(a, o),
		newPortGroupSetCommand(a, o),
		newPortGroupUnsetCommand(a, o),
		newPortGroupDeleteCommand(a, o),
	)
	return cmd
}

type portGroupListFlags struct {
	long    bool
	node    string
	address string
	shards  []string
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func newPortGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portGroupListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal port groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortGroupList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output, including member ports")
	fl.StringVar(&f.node, "node", "", "limit to port groups of this node (name or UUID)")
	fl.StringVar(&f.address, "address", "", "limit to the port group with this MAC address")
	fl.StringSliceVar(&f.shards, "shards", nil, "limit to port groups of nodes in these shards (requires ironic API 1.106)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of port groups to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last port group from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this port group attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runPortGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portGroupListFlags, w io.Writer) error {
	q := url.Values{
		"node": {f.node}, "address": {f.address}, "marker": {f.marker},
		"sort_key": {f.sortKey}, "sort_dir": {f.sortDir},
	}
	if len(f.shards) > 0 {
		q.Set("shard", strings.Join(f.shards, ","))
	}
	fields, path := portGroupFields, "portgroups"
	if f.long || len(o.SelectedColumns(labels(portGroupDetailFields)...)) > len(o.SelectedColumns(labels(portGroupFields)...)) {
		fields, path = portGroupDetailFields, "portgroups/detail"
	}
	groups, err := ironicList(ctx, client, path, "portgroups", q, f.limit)
	if err != nil {
		return fmt.Errorf("listing baremetal port groups: %w", err)
	}
	if slices.Contains(fields, "ports") && len(groups) > 0 && (len(o.Columns) == 0 || len(o.SelectedColumns("Ports")) > 0) {
		if err := fillMemberPorts(ctx, client, f.node, groups); err != nil {
			return err
		}
	}
	return o.WriteList(w, listTable(groups, fields))
}

// fillMemberPorts sets each group's "ports" to its member port UUIDs, read in
// one port listing rather than one request per group.
func fillMemberPorts(ctx context.Context, client *gophercloud.ServiceClient, node string, groups []map[string]any) error {
	members, err := ironicList(ctx, client, "ports", "ports",
		url.Values{"node": {node}, "fields": {"uuid,portgroup_uuid"}}, 0)
	if err != nil {
		return fmt.Errorf("listing member ports: %w", err)
	}
	byGroup := map[string][]string{}
	for _, p := range members {
		if g, _ := p["portgroup_uuid"].(string); g != "" {
			id, _ := p["uuid"].(string)
			byGroup[g] = append(byGroup[g], id)
		}
	}
	for _, g := range groups {
		id, _ := g["uuid"].(string)
		g["ports"] = byGroup[id]
	}
	return nil
}

func newPortGroupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var byAddress bool
	cmd := &cobra.Command{
		Use:   "show <port group>",
		Short: "Show details of a baremetal port group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortGroupShow(ctx, client, o, args[0], byAddress, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&byAddress, "address", false, "<port group> is the MAC address (instead of the UUID or name) of the port group")
	addFieldsAliases(cmd, o)
	return cmd
}

func runPortGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, byAddress bool, w io.Writer) error {
	pg, err := getPortGroup(ctx, client, ref, byAddress)
	if err != nil {
		return err
	}
	return writePortGroup(ctx, client, o, pg, w)
}

// getPortGroup reads a port group by UUID or name, or by MAC address.
func getPortGroup(ctx context.Context, client *gophercloud.ServiceClient, ref string, byAddress bool) (map[string]any, error) {
	if !byAddress {
		var pg map[string]any
		if err := ironicGet(ctx, client, joinPath("portgroups", ref), &pg); err != nil {
			return nil, fmt.Errorf("getting baremetal port group %s: %w", ref, err)
		}
		return pg, nil
	}
	groups, err := ironicList(ctx, client, "portgroups/detail", "portgroups", url.Values{"address": {ref}}, 0)
	if err != nil {
		return nil, fmt.Errorf("getting baremetal port group with address %s: %w", ref, err)
	}
	if len(groups) != 1 {
		return nil, fmt.Errorf("no baremetal port group found with address %q", ref)
	}
	return groups[0], nil
}

// writePortGroup renders a port group with its member ports.
func writePortGroup(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, pg map[string]any, w io.Writer) error {
	if len(o.Columns) == 0 || len(o.SelectedColumns("ports")) > 0 {
		id, _ := pg["uuid"].(string)
		members, err := ironicList(ctx, client, joinPath("portgroups", id, "ports"), "ports", nil, 0)
		if err != nil {
			return fmt.Errorf("listing ports of port group %s: %w", id, err)
		}
		uuids := make([]string, 0, len(members))
		for _, p := range members {
			s, _ := p["uuid"].(string)
			uuids = append(uuids, s)
		}
		pg["ports"] = uuids
	}
	fields, values := showFields(pg, portGroupDetailFields)
	return o.WriteSingle(w, fields, values)
}

// portGroupAttrFlags are the attributes create and set share.
type portGroupAttrFlags struct {
	node            string
	address         string
	name            string
	mode            string
	physicalNetwork string
	category        string
	extra           []string
	properties      []string
	standalone      bool
	noStandalone    bool
}

func (f *portGroupAttrFlags) register(cmd *cobra.Command, verb string) {
	fl := cmd.Flags()
	fl.StringVar(&f.address, "address", "", "MAC address for this port group")
	fl.StringVar(&f.name, "name", "", "name of the port group")
	fl.StringVar(&f.mode, "mode", "", "bonding mode, e.g. 802.3ad, balance-rr, active-backup")
	fl.StringVar(&f.physicalNetwork, "physical-network", "",
		"physical network of the port group's ports (requires ironic API 1.102, OpenStack 2026.1)")
	fl.StringVar(&f.category, "category", "", "category of the port group (requires ironic API 1.103, OpenStack 2026.1)")
	fl.StringArrayVar(&f.extra, "extra", nil, verb+" arbitrary metadata key=value (repeatable)")
	fl.StringArrayVar(&f.properties, "property", nil, verb+" a bonding property key=value, e.g. miimon=100 (repeatable)")
	fl.BoolVar(&f.standalone, "support-standalone-ports", false, "member ports may also be used as standalone ports")
	fl.BoolVar(&f.noStandalone, "unsupport-standalone-ports", false, "member ports may not be used as standalone ports")
	cmd.MarkFlagsMutuallyExclusive("support-standalone-ports", "unsupport-standalone-ports")
}

// explain names the microversion a rejected request needed, for the gated
// attributes it carried.
func (f *portGroupAttrFlags) explain(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	switch {
	case f.physicalNetwork != "":
		return explainMicroversion(ctx, client, featurePortGroupPhysnet, err)
	case f.category != "":
		return explainMicroversion(ctx, client, featurePortGroupCategory, err)
	}
	return err
}

func newPortGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portGroupAttrFlags{}
	var uuid string
	cmd := &cobra.Command{
		Use:   "create --node <node>",
		Short: "Create a new baremetal port group",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortGroupCreate(ctx, client, o, f, uuid, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.node, "node", "", "node this port group belongs to (name or UUID, required)")
	cmd.Flags().StringVar(&uuid, "uuid", "", "UUID of the port group")
	f.register(cmd, "set")
	_ = cmd.MarkFlagRequired("node")
	return cmd
}

func runPortGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *portGroupAttrFlags, uuid string, w io.Writer,
) error {
	if err := o.CheckColumns(portGroupColumns...); err != nil {
		return err
	}
	node, err := nodeUUID(ctx, client, f.node)
	if err != nil {
		return err
	}
	body := map[string]any{"node_uuid": node}
	setIf(body, "uuid", uuid)
	setIf(body, "address", f.address)
	setIf(body, "name", f.name)
	setIf(body, "mode", f.mode)
	setIf(body, "physical_network", f.physicalNetwork)
	setIf(body, "category", f.category)
	if f.standalone || f.noStandalone {
		body["standalone_ports_supported"] = f.standalone
	}
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	props, err := parseKeyValMap(f.properties)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	if extra != nil {
		body["extra"] = extra
	}
	if props != nil {
		body["properties"] = props
	}
	var pg map[string]any
	if err := ironicPost(ctx, client, "portgroups", body, &pg); err != nil {
		return fmt.Errorf("creating baremetal port group: %w", f.explain(ctx, client, err))
	}
	return writePortGroup(ctx, client, o, pg, w)
}

// nodeUUID resolves a node name to its UUID; a UUID-shaped ref is read too,
// which also proves it exists before it is written into another resource.
func nodeUUID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	var n struct {
		UUID string `json:"uuid"`
	}
	if err := ironicGet(ctx, client, joinPath("nodes", ref)+"?fields=uuid", &n); err != nil {
		return "", fmt.Errorf("resolving baremetal node %s: %w", ref, err)
	}
	return n.UUID, nil
}

func newPortGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portGroupAttrFlags{}
	cmd := &cobra.Command{
		Use:   "set <port group>",
		Short: "Set baremetal port group properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortGroupSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.node, "node", "", "move the port group to this node (name or UUID)")
	f.register(cmd, "set")
	return cmd
}

func runPortGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *portGroupAttrFlags, w io.Writer,
) error {
	if err := o.CheckColumns(portGroupColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	if f.node != "" {
		node, err := nodeUUID(ctx, client, f.node)
		if err != nil {
			return err
		}
		ops.replace("/node_uuid", node)
	}
	for path, v := range map[string]string{
		"/address": f.address, "/name": f.name, "/mode": f.mode,
		"/physical_network": f.physicalNetwork, "/category": f.category,
	} {
		if v != "" {
			ops.replace(path, v)
		}
	}
	if f.standalone || f.noStandalone {
		ops.replace("/standalone_ports_supported", f.standalone)
	}
	if err := ops.addKV("/extra", "extra", f.extra); err != nil {
		return err
	}
	if err := ops.addKV("/properties", "property", f.properties); err != nil {
		return err
	}
	if len(ops) == 0 {
		return fmt.Errorf("port group set requires at least one attribute flag")
	}
	slices.SortStableFunc(ops, func(a, b patchOp) int { return strings.Compare(a.Path, b.Path) })
	var pg map[string]any
	if err := ironicPatch(ctx, client, joinPath("portgroups", ref), ops, &pg); err != nil {
		return fmt.Errorf("updating baremetal port group %s: %w", ref, f.explain(ctx, client, err))
	}
	return writePortGroup(ctx, client, o, pg, w)
}

type portGroupUnsetFlags struct {
	name, address, physicalNetwork, category bool
	extra, properties                        []string
}

func newPortGroupUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portGroupUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <port group>",
		Short: "Unset baremetal port group properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortGroupUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.name, "name", false, "unset the name of the port group")
	fl.BoolVar(&f.address, "address", false, "unset the address of the port group")
	fl.BoolVar(&f.physicalNetwork, "physical-network", false, "unset the physical network of the port group")
	fl.BoolVar(&f.category, "category", false, "unset the category of the port group")
	fl.StringArrayVar(&f.extra, "extra", nil, "extra key to unset (repeatable)")
	fl.StringArrayVar(&f.properties, "property", nil, "property key to unset (repeatable)")
	return cmd
}

func runPortGroupUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *portGroupUnsetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(portGroupColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	for path, set := range map[string]bool{
		"/name": f.name, "/address": f.address, "/physical_network": f.physicalNetwork, "/category": f.category,
	} {
		if set {
			ops.remove(path)
		}
	}
	slices.SortStableFunc(ops, func(a, b patchOp) int { return strings.Compare(a.Path, b.Path) })
	ops.removeKeys("/extra", f.extra)
	ops.removeKeys("/properties", f.properties)
	if len(ops) == 0 {
		return fmt.Errorf("port group unset requires at least one attribute flag")
	}
	var pg map[string]any
	if err := ironicPatch(ctx, client, joinPath("portgroups", ref), ops, &pg); err != nil {
		return fmt.Errorf("updating baremetal port group %s: %w", ref, err)
	}
	return writePortGroup(ctx, client, o, pg, w)
}

func newPortGroupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <port group> [<port group> ...]",
		Short: "Delete baremetal port group(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortGroupDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runPortGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		if err := ironicDelete(ctx, client, joinPath("portgroups", ref)); err != nil {
			return fmt.Errorf("deleting baremetal port group %s: %w", ref, err)
		}
		_, err := fmt.Fprintf(w, "Deleted port group %s\n", ref)
		return err
	})
}
