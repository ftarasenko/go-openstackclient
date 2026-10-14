package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "baremetal node network show" is koc-native: one read-only view of how a
// node is wired, joining ironic (ports, port groups, the switch each one is
// cabled to, the VIF bound to it) with neutron (the VIF's network, VLAN,
// binding and trunk subports). Debugging a bare-metal binding otherwise takes
// a port list, a port group list and a neutron show per VIF, per subport and
// per network.

// nodeNetworkColumns are the table's columns. A row is an ironic port group,
// an ironic port, or a trunk subport of the VIF above it.
var nodeNetworkColumns = []string{
	"Kind", "Ironic", "Name", "MAC", "PXE", "Physical Network", "Port Group", "Switch", "Switch Port",
	"VIF", "Network", "VLAN", "Status", "VIF Type", "Bound Drivers", "Trunk",
}

func newNodeNetworkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	show := &cobra.Command{
		Use:   "show <node>",
		Short: "Show how a node is wired: ironic ports and port groups, their switch ports, VIFs, networks and trunk subports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			ironic, ac, err := a.NewServiceSession(ctx, (*auth.Client).Baremetal)
			if err != nil {
				return err
			}
			// Without neutron (--creds-from-ns is ironic-only) the ironic half
			// is still worth showing.
			neutron, err := ac.Network()
			if err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: neutron unavailable, showing the ironic side only: %v\n", err)
				neutron = nil
			}
			return runNodeNetworkShow(ctx, ironic, neutron, o, args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Show a node's network wiring (ironic and neutron)",
	}
	cmd.AddCommand(show)
	return cmd
}

// nodeWiring is everything read for one node.
type nodeWiring struct {
	neutron  bool // whether the neutron side was read
	node     map[string]any
	ports    []map[string]any
	groups   []map[string]any
	vifs     map[string]map[string]any // neutron port by ID
	networks map[string]map[string]any // neutron network by ID
	trunks   map[string]map[string]any // neutron trunk by ID
}

func runNodeNetworkShow(ctx context.Context, ironic, neutron *gophercloud.ServiceClient, o *output.Options,
	ref string, w, ew io.Writer,
) error {
	if err := o.CheckColumns(nodeNetworkColumns...); err != nil {
		return err
	}
	nw, err := readNodeWiring(ctx, ironic, neutron, ref)
	if err != nil {
		return err
	}
	if err := o.WriteList(w, nw.table()); err != nil {
		return err
	}
	for _, warn := range nw.check() {
		if _, err := fmt.Fprintf(ew, "warning: %s\n", warn); err != nil {
			return err
		}
	}
	return nil
}

func readNodeWiring(ctx context.Context, ironic, neutron *gophercloud.ServiceClient, ref string) (*nodeWiring, error) {
	nw := &nodeWiring{vifs: map[string]map[string]any{}, networks: map[string]map[string]any{}, trunks: map[string]map[string]any{}}
	if err := ironicGet(ctx, ironic, joinPath("nodes", ref), &nw.node); err != nil {
		return nil, fmt.Errorf("getting baremetal node %s: %w", ref, err)
	}
	node := str(nw.node, "uuid")
	var err error
	if nw.ports, err = ironicList(ctx, ironic, "ports/detail", "ports", url.Values{"node": {node}}, 0); err != nil {
		return nil, fmt.Errorf("listing ports of baremetal node %s: %w", ref, err)
	}
	if nw.groups, err = ironicList(ctx, ironic, "portgroups/detail", "portgroups", url.Values{"node": {node}}, 0); err != nil {
		return nil, fmt.Errorf("listing port groups of baremetal node %s: %w", ref, err)
	}
	if neutron == nil {
		return nw, nil
	}
	nw.neutron = true
	for _, r := range slices.Concat(nw.groups, nw.ports) {
		if err := nw.readVIF(ctx, neutron, vifOf(r)); err != nil {
			return nil, err
		}
	}
	return nw, nil
}

// readVIF reads a VIF, its network, and — when it is a trunk parent — the
// trunk and every subport with its network. A VIF neutron no longer has is
// left out, and check reports it.
func (nw *nodeWiring) readVIF(ctx context.Context, neutron *gophercloud.ServiceClient, id string) error {
	if id == "" || nw.vifs[id] != nil {
		return nil
	}
	port, err := neutronGet(ctx, neutron, "ports", id, "port")
	if err != nil || port == nil {
		return err
	}
	nw.vifs[id] = port
	if err := nw.readNetwork(ctx, neutron, str(port, "network_id")); err != nil {
		return err
	}
	td, _ := port["trunk_details"].(map[string]any)
	trunkID := str(td, "trunk_id")
	if trunkID == "" {
		return nil
	}
	trunk, err := neutronGet(ctx, neutron, "trunks", trunkID, "trunk")
	if err != nil {
		return err
	}
	if trunk == nil {
		return nil
	}
	nw.trunks[trunkID] = trunk
	for _, sp := range subports(trunk) {
		sub, err := neutronGet(ctx, neutron, "ports", str(sp, "port_id"), "port")
		if err != nil {
			return err
		}
		if sub == nil {
			continue
		}
		nw.vifs[str(sub, "id")] = sub
		if err := nw.readNetwork(ctx, neutron, str(sub, "network_id")); err != nil {
			return err
		}
	}
	return nil
}

func (nw *nodeWiring) readNetwork(ctx context.Context, neutron *gophercloud.ServiceClient, id string) error {
	if id == "" || nw.networks[id] != nil {
		return nil
	}
	n, err := neutronGet(ctx, neutron, "networks", id, "network")
	if err != nil || n == nil {
		return err
	}
	nw.networks[id] = n
	return nil
}

// neutronGet reads /v2.0/<collection>/<id>, nil when neutron has no such
// resource.
func neutronGet(ctx context.Context, neutron *gophercloud.ServiceClient, collection, id, key string) (map[string]any, error) {
	var body map[string]map[string]any
	resp, err := neutron.Get(ctx, neutron.ServiceURL(collection, id), &body, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting neutron %s %s: %w", strings.TrimSuffix(collection, "s"), id, err)
	}
	return body[key], nil
}

// vifOf is the tenant VIF bound to an ironic port or port group: ironic's
// internal_info since Pike, extra.vif_port_id before it.
func vifOf(r map[string]any) string {
	ii, _ := r["internal_info"].(map[string]any)
	if v := str(ii, "tenant_vif_port_id"); v != "" {
		return v
	}
	ex, _ := r["extra"].(map[string]any)
	return str(ex, "vif_port_id")
}

func subports(trunk map[string]any) []map[string]any {
	raw, _ := trunk["sub_ports"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		if m, ok := s.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// str reads a string member of a decoded JSON object; "" when absent.
func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// table lays out port groups first, each followed by its member ports and its
// VIF's subports, then the standalone ports and their subports.
func (nw *nodeWiring) table() output.Table {
	t := output.Table{Columns: nodeNetworkColumns}
	groupName := map[string]string{}
	for _, g := range nw.groups {
		groupName[str(g, "uuid")] = firstNonEmpty(str(g, "name"), str(g, "uuid"))
	}
	for _, g := range nw.groups {
		t.Rows = append(t.Rows, nw.row("portgroup", g, ""))
		for _, p := range nw.ports {
			if str(p, "portgroup_uuid") == str(g, "uuid") {
				t.Rows = append(t.Rows, nw.row("port", p, groupName[str(g, "uuid")]))
			}
		}
		t.Rows = append(t.Rows, nw.subportRows(vifOf(g))...)
	}
	for _, p := range nw.ports {
		if pg := str(p, "portgroup_uuid"); pg != "" && groupName[pg] != "" {
			continue
		}
		t.Rows = append(t.Rows, nw.row("port", p, ""))
		t.Rows = append(t.Rows, nw.subportRows(vifOf(p))...)
	}
	return t
}

func (nw *nodeWiring) row(kind string, r map[string]any, group string) []any {
	llc, _ := r["local_link_connection"].(map[string]any)
	row := []any{
		kind, str(r, "uuid"), str(r, "name"), str(r, "address"), r["pxe_enabled"], str(r, "physical_network"), group,
		firstNonEmpty(str(llc, "switch_info"), str(llc, "switch_id")), str(llc, "port_id"),
		vifOf(r),
	}
	return append(row, nw.vifCells(vifOf(r))...)
}

// vifCells are the neutron columns for a VIF: Network, VLAN, Status, VIF Type,
// Bound Drivers, Trunk.
func (nw *nodeWiring) vifCells(id string) []any {
	port := nw.vifs[id]
	if port == nil {
		return []any{nil, nil, nil, nil, nil, nil}
	}
	net := nw.networks[str(port, "network_id")]
	td, _ := port["trunk_details"].(map[string]any)
	return []any{
		firstNonEmpty(str(net, "name"), str(port, "network_id")), net["provider:segmentation_id"],
		port["status"], port["binding:vif_type"], boundDrivers(port), str(td, "trunk_id"),
	}
}

func (nw *nodeWiring) subportRows(parentVIF string) [][]any {
	parent := nw.vifs[parentVIF]
	td, _ := parent["trunk_details"].(map[string]any)
	trunk := nw.trunks[str(td, "trunk_id")]
	var rows [][]any
	for _, sp := range subports(trunk) {
		id := str(sp, "port_id")
		sub := nw.vifs[id]
		net := nw.networks[str(sub, "network_id")]
		rows = append(rows, []any{
			"subport", "", str(sub, "name"), str(sub, "mac_address"), nil, "", "", "", "",
			id, firstNonEmpty(str(net, "name"), str(sub, "network_id")), sp["segmentation_id"],
			sub["status"], sub["binding:vif_type"], boundDrivers(sub), str(trunk, "id"),
		})
	}
	return rows
}

// boundDrivers renders ML2's binding:vif_details.bound_drivers, a map from
// binding level to mechanism driver, in level order.
func boundDrivers(port map[string]any) string {
	vd, _ := port["binding:vif_details"].(map[string]any)
	bd, _ := vd["bound_drivers"].(map[string]any)
	levels := slices.Sorted(func(yield func(string) bool) {
		for k := range bd {
			if !yield(k) {
				return
			}
		}
	})
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		if s, ok := bd[l].(string); ok {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// check reports the wiring faults that break bare-metal networking silently.
func (nw *nodeWiring) check() []string {
	var warns []string
	members := map[string][]map[string]any{}
	for _, p := range nw.ports {
		members[str(p, "portgroup_uuid")] = append(members[str(p, "portgroup_uuid")], p)
	}
	for _, r := range slices.Concat(nw.groups, nw.ports) {
		vif := vifOf(r)
		if vif == "" {
			continue
		}
		what := "port " + firstNonEmpty(str(r, "name"), str(r, "address"), str(r, "uuid"))
		cabled := []map[string]any{r}
		if g, ok := members[str(r, "uuid")]; ok {
			what, cabled = "port group "+firstNonEmpty(str(r, "name"), str(r, "uuid")), g
		}
		for _, p := range cabled {
			if llc, _ := p["local_link_connection"].(map[string]any); len(llc) == 0 {
				warns = append(warns, fmt.Sprintf("VIF %s is on %s, but ironic port %s has no local_link_connection: "+
					"the switch port cannot be configured", vif, what, firstNonEmpty(str(p, "address"), str(p, "uuid"))))
			}
		}
		if !nw.neutron {
			continue
		}
		port := nw.vifs[vif]
		if port == nil {
			// neutron answers 404 for a port this token cannot see too: a
			// system-scoped token, which ironic's defaults want, sees no
			// project's ports.
			warns = append(warns, fmt.Sprintf("VIF %s on %s was not found in neutron: deleted, "+
				"or not visible to this token (neutron shows a system-scoped token no project's ports)", vif, what))
			continue
		}
		switch vt := str(port, "binding:vif_type"); vt {
		case "binding_failed", "unbound":
			warns = append(warns, fmt.Sprintf("VIF %s on %s: binding:vif_type is %s", vif, what, vt))
		}
		nw.checkTrunk(port, &warns)
	}
	return warns
}

func (nw *nodeWiring) checkTrunk(parent map[string]any, warns *[]string) {
	td, _ := parent["trunk_details"].(map[string]any)
	trunk := nw.trunks[str(td, "trunk_id")]
	if trunk == nil {
		return
	}
	for _, sp := range subports(trunk) {
		sub := nw.vifs[str(sp, "port_id")]
		if sub == nil {
			continue
		}
		if mac := str(sub, "mac_address"); mac != "" && !strings.EqualFold(mac, str(parent, "mac_address")) {
			*warns = append(*warns, fmt.Sprintf("subport %s (VLAN %v) has MAC %s, parent %s has %s: "+
				"a bond drops frames addressed to it", str(sub, "id"), sp["segmentation_id"], mac, str(parent, "id"), str(parent, "mac_address")))
		}
		if str(trunk, "status") == "ACTIVE" && str(sub, "status") == "DOWN" {
			*warns = append(*warns, fmt.Sprintf("trunk %s is ACTIVE but subport %s (VLAN %v) is DOWN",
				str(trunk, "id"), str(sub, "id"), sp["segmentation_id"]))
		}
	}
}
