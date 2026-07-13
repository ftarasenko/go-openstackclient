package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
)

// resolveServerCreateRefs turns the human-friendly --image (glance) and
// --network (neutron) references on a create request into IDs, deriving the
// image and network clients lazily from the shared session so those services
// are only contacted when a non-UUID name is actually supplied.
func resolveServerCreateRefs(ctx context.Context, session *auth.Client, f *serverCreateFlags) error {
	if f.image != "" && !isUUID(f.image) {
		img, err := session.Image()
		if err != nil {
			return err
		}
		id, err := resolve.ImageID(ctx, img, f.image)
		if err != nil {
			return err
		}
		f.image = id
	}
	for i := range f.nicSpecs {
		n := f.nicSpecs[i].netRef
		if n == "" || isUUID(n) {
			continue
		}
		net, err := session.Network()
		if err != nil {
			return err
		}
		id, err := resolve.NetworkID(ctx, net, n)
		if err != nil {
			return err
		}
		f.nicSpecs[i].netRef = id
	}
	return nil
}

// nicSpec is a parsed --nic / --network value. netRef holds a network ID or
// name (resolved to a UUID by resolveServerCreateRefs); port and fixedIP are
// optional.
type nicSpec struct {
	netRef  string
	port    string
	fixedIP string
}

// parseNIC parses one --nic / --network value. A bare value (no '=') is treated
// as a network ID or name, for backward compatibility. Otherwise it accepts the
// upstream OSC comma-separated key=value form, e.g.
// "net-id=<id>,v4-fixed-ip=<ip>" or "port-id=<id>". Recognized keys:
// net-id/net-name/network/uuid → network ref (id or name), port-id/port →
// neutron port, v4-fixed-ip/v6-fixed-ip/fixed-ip → fixed address.
func parseNIC(s string) (nicSpec, error) {
	if !strings.Contains(s, "=") {
		return nicSpec{netRef: s}, nil
	}
	var spec nicSpec
	for _, part := range strings.Split(s, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, v, err := parseKeyVal(part)
		if err != nil {
			return nicSpec{}, fmt.Errorf("invalid --nic %q: %w", s, err)
		}
		switch strings.TrimSpace(k) {
		case "net-id", "net-name", "network", "uuid":
			spec.netRef = v
		case "port-id", "port":
			spec.port = v
		case "v4-fixed-ip", "v6-fixed-ip", "fixed-ip":
			spec.fixedIP = v
		default:
			return nicSpec{}, fmt.Errorf("invalid --nic %q: unknown key %q", s, k)
		}
	}
	if spec.netRef == "" && spec.port == "" {
		return nicSpec{}, fmt.Errorf("invalid --nic %q: needs net-id, net-name, or port-id", s)
	}
	return spec, nil
}

// isUUID reports whether s looks like a canonical UUID. It reuses the shared
// resolver's matcher so there is one definition of "looks like a UUID".
func isUUID(s string) bool {
	return resolve.IsUUID(s)
}

// parseKeyVal splits a "key=value" string into its two halves. The value may
// itself contain '=' signs; only the first is treated as the separator.
func parseKeyVal(s string) (string, string, error) {
	i := strings.Index(s, "=")
	if i < 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	key := strings.TrimSpace(s[:i])
	if key == "" {
		return "", "", fmt.Errorf("empty key in %q", s)
	}
	return key, s[i+1:], nil
}

// parseKeyValStrings turns a slice of "key=value" flag values into a map.
func parseKeyValStrings(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}

// resolveServerID accepts either a server ID or a server name and returns the
// server's ID. Nova's server-scoped endpoints all key on the ID, while operators
// routinely pass names, so this performs the name→ID lookup where required. A
// value that already looks like a UUID is used verbatim to avoid an extra call.
func resolveServerID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if isUUID(ref) {
		return ref, nil
	}
	// AllTenants lets an admin token resolve a server owned by another project
	// (write verbs like delete/stop must work cross-project). Nova silently
	// ignores all_tenants for non-admin tokens, so setting it here is safe and
	// does not broaden a regular user's visibility.
	pages, err := servers.List(client, servers.ListOpts{Name: ref, AllTenants: true}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving server %q: %w", ref, err)
	}
	all, err := servers.ExtractServers(pages)
	if err != nil {
		return "", fmt.Errorf("resolving server %q: %w", ref, err)
	}
	var matches []servers.Server
	for _, s := range all {
		if s.Name == ref {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no server found with name %q", ref)
	case 1:
		return matches[0].ID, nil
	default:
		return "", fmt.Errorf("more than one server matches name %q; specify the ID", ref)
	}
}

// resolveFlavorRef accepts either a flavor ID or a flavor name and returns the
// flavor ID that nova expects in flavorRef. Flavor IDs are frequently short,
// non-UUID strings (e.g. "1"), so this always consults the flavor list. An
// exact ID match wins immediately; otherwise name matches are collected and an
// ambiguous name (more than one match) is rejected rather than silently picking
// the first — matching compute/flavor.go resolveFlavorID semantics.
func resolveFlavorRef(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	pages, err := flavors.ListDetail(client, nil).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return "", fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	var byName []string
	for _, f := range all {
		if f.ID == ref {
			return f.ID, nil
		}
		if f.Name == ref {
			byName = append(byName, f.ID)
		}
	}
	switch len(byName) {
	case 0:
		return "", fmt.Errorf("no flavor found matching %q", ref)
	case 1:
		return byName[0], nil
	default:
		return "", fmt.Errorf("more than one flavor named %q; specify the ID", ref)
	}
}

// formatNetworks renders a server's address map (keyed by network name) into a
// compact "net=ip, ip" string, matching the Networks column of `openstack
// server list`.
func formatNetworks(addresses map[string]any) string {
	if len(addresses) == 0 {
		return ""
	}
	nets := make([]string, 0, len(addresses))
	for name := range addresses {
		nets = append(nets, name)
	}
	sort.Strings(nets)
	parts := make([]string, 0, len(nets))
	for _, name := range nets {
		var ips []string
		if list, ok := addresses[name].([]any); ok {
			for _, entry := range list {
				if m, ok := entry.(map[string]any); ok {
					if addr, ok := m["addr"].(string); ok {
						ips = append(ips, addr)
					}
				}
			}
		}
		parts = append(parts, fmt.Sprintf("%s=%s", name, strings.Join(ips, ", ")))
	}
	return strings.Join(parts, "; ")
}

// flavorName extracts a human-readable flavor name from the server's embedded
// flavor object (the "original_name" key, present from microversion 2.47).
func flavorName(flavor map[string]any) string {
	if flavor == nil {
		return ""
	}
	if n, ok := flavor["original_name"].(string); ok {
		return n
	}
	if n, ok := flavor["id"].(string); ok {
		return n
	}
	return ""
}

// imageID extracts the image ID from the server's embedded image object.
func imageID(image map[string]any) string {
	if image == nil {
		return ""
	}
	if id, ok := image["id"].(string); ok {
		return id
	}
	return ""
}
