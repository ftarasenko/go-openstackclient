package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serverColumn is one optional "server list" column: the header and how to read
// it off a listing entry.
type serverColumn struct {
	Name  string
	Value func(servers.Server) any
	// Raw names a server attribute gophercloud's Server does not model; the
	// column is read from the raw listing instead of Value.
	Raw string
	// Microversion is the least nova version that returns Raw.
	Microversion string
}

// serverListOptional are the columns "server list" renders only when
// -c/--column (or --sort-column) names one, mirroring the opt-in extras
// upstream's ListServer appends (`compute/v2/server.py`, the
// `if parsed_args.columns:` block). They are deliberately outside the default
// listing — widening it would change every script that reads it positionally —
// and --long carries the ones upstream's --long does too, which
// serverListExtraColumns then skips.
//
// Creation time is the one that matters most in practice — without it there is
// no way to get a server's age from a listing at all, and the fallback is one
// `server show` per server.
//
// Host Status, Pinned Availability Zone and Scheduler Hints are fields nova
// adds at 2.16, 2.96 and 2.100. Naming one raises the listing to that
// microversion, which a cloud older than it refuses.
var serverListOptional = []serverColumn{
	{Name: "Created At", Value: func(s servers.Server) any { return s.Created }},
	{Name: "Image ID", Value: func(s servers.Server) any { return listImageID(s) }},
	{Name: "Flavor ID", Value: func(s servers.Server) any { return flavorID(s.Flavor) }},
	{Name: "Availability Zone", Value: func(s servers.Server) any { return s.AvailabilityZone }},
	{Name: "Host", Value: func(s servers.Server) any { return s.HypervisorHostname }},
	{Name: "Task State", Value: func(s servers.Server) any { return s.TaskState }},
	{Name: "Power State", Value: func(s servers.Server) any { return powerStateLabel(int(s.PowerState)) }},
	{Name: "Project ID", Value: func(s servers.Server) any { return s.TenantID }},
	{Name: "User ID", Value: func(s servers.Server) any { return s.UserID }},
	{Name: "Security Groups", Value: func(s servers.Server) any { return securityGroupNames(s.SecurityGroups) }},
	{Name: "Properties", Value: func(s servers.Server) any { return formatServerMetadata(s.Metadata) }},
	{Name: "Host Status", Raw: "host_status", Microversion: "2.16"},
	{Name: "Pinned Availability Zone", Raw: "pinned_availability_zone", Microversion: "2.96"},
	{Name: "Scheduler Hints", Raw: "scheduler_hints", Microversion: "2.100"},
}

// serverListExtraColumns returns the optional columns the user asked for and
// the base listing does not already carry, so selecting one that --long
// already renders is a no-op rather than a duplicated header.
func serverListExtraColumns(o *output.Options, present []string) []serverColumn {
	names := make([]string, 0, len(serverListOptional))
	for _, c := range serverListOptional {
		names = append(names, c.Name)
	}
	var extra []serverColumn
	for _, want := range o.SelectedColumns(names...) {
		if hasColumn(present, want) {
			continue
		}
		for _, c := range serverListOptional {
			if c.Name == want {
				extra = append(extra, c)
				break
			}
		}
	}
	return extra
}

// serverListOptionalNames lists every optional column, for the flag help and
// for the error a bad -c name produces.
func serverListOptionalNames() []string {
	names := make([]string, 0, len(serverListOptional))
	for _, c := range serverListOptional {
		names = append(names, c.Name)
	}
	return names
}

func hasColumn(cols []string, name string) bool {
	for _, c := range cols {
		if strings.EqualFold(c, name) {
			return true
		}
	}
	return false
}

// flavorID reads the embedded flavor's ID. Nova drops it from the listing at
// microversion 2.47 in favour of the inlined flavor definition, so this is
// empty on a client that negotiated 2.47+ — the same gap upstream papers over
// by exposing Flavor ID only below 2.47.
func flavorID(flavor map[string]any) string {
	if id, ok := flavor["id"].(string); ok {
		return id
	}
	return ""
}

// securityGroupNames renders the listing's security groups as upstream does
// (`security_groups_name`): the names, comma-separated. Nova repeats a group
// once per port it is applied to, so duplicates are collapsed — the column
// answers "which groups", not "how many ports".
func securityGroupNames(groups []map[string]any) string {
	seen := make(map[string]struct{}, len(groups))
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		name, ok := g["name"].(string)
		if !ok || name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// formatServerMetadata renders server metadata as a stable, comma-separated
// "key='value'" string, matching OSC's Properties column (same form as
// formatAggregateMetadata).
func formatServerMetadata(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"='"+m[k]+"'")
	}
	return strings.Join(pairs, ", ")
}

// serverListColumnAliases are upstream's attribute spellings of the listing's
// headers, which its ListServer accepts in -c as well.
var serverListColumnAliases = map[string]string{
	"id": "ID", "name": "Name", "status": "Status", "networks": "Networks", "addresses": "Networks",
	"image_id": "Image ID", "flavor": "Flavor", "flavor_name": "Flavor", "flavor_id": "Flavor ID",
	"created_at": "Created At", "availability_zone": "Availability Zone", "host": "Host",
	"task_state": "Task State", "power_state": "Power State", "project_id": "Project ID",
	"user_id": "User ID", "security_groups": "Security Groups", "properties": "Properties",
	"metadata": "Properties", "host_status": "Host Status",
	"pinned_availability_zone": "Pinned Availability Zone", "scheduler_hints": "Scheduler Hints",
}

// aliasServerListColumns rewrites -c and --sort-column names given in
// upstream's attribute spelling to the header they select. The image name is
// "Image" in the default listing and "Image Name" in --long; either spelling
// selects whichever is shown.
func aliasServerListColumns(o *output.Options, long bool) {
	image := "Image"
	if long {
		image = "Image Name"
	}
	alias := func(names []string) {
		for i, c := range names {
			key := strings.ToLower(strings.TrimSpace(c))
			switch {
			case key == "image" || key == "image name" || key == "image_name":
				names[i] = image
			case serverListColumnAliases[key] != "":
				names[i] = serverListColumnAliases[key]
			}
		}
	}
	alias(o.Columns)
	alias(o.SortColumns)
}

// serverListRawColumnNames are the optional columns read from the raw listing.
func serverListRawColumnNames() []string {
	var names []string
	for _, c := range serverListOptional {
		if c.Raw != "" {
			names = append(names, c.Name)
		}
	}
	return names
}

// rawColumnsMicroversion is the least microversion that returns every raw
// column named, or "" when none is.
func rawColumnsMicroversion(names []string) string {
	mv := ""
	for _, c := range serverListOptional {
		if c.Raw != "" && hasColumn(names, c.Name) && (mv == "" || microversionLess(mv, c.Microversion)) {
			mv = c.Microversion
		}
	}
	return mv
}

// microversionLess reports whether a is an older microversion than b.
func microversionLess(a, b string) bool {
	aMaj, aMin, _ := parseMicroversion(a)
	bMaj, bMin, _ := parseMicroversion(b)
	if aMaj != bMaj {
		return aMaj < bMaj
	}
	return aMin < bMin
}

// extractServersWithRaw extracts a page as ExtractServers does and also keeps
// each server's raw attributes, keyed by ID, for the raw columns.
func extractServersWithRaw(raw map[string]map[string]any) func(pagination.Page) ([]servers.Server, error) {
	return func(page pagination.Page) ([]servers.Server, error) {
		list, err := servers.ExtractServers(page)
		if err != nil {
			return nil, err
		}
		var body struct {
			Servers []map[string]any `json:"servers"`
		}
		sp, ok := page.(servers.ServerPage)
		if !ok {
			return nil, fmt.Errorf("unexpected page type %T", page)
		}
		if err := sp.ExtractInto(&body); err != nil {
			return nil, err
		}
		for _, m := range body.Servers {
			if id, ok := m["id"].(string); ok {
				raw[id] = m
			}
		}
		return list, nil
	}
}

// listImageID is the listing's image ID, or upstream's marker for a server
// booted from a volume.
func listImageID(s servers.Server) string {
	if id := imageID(s.Image); id != "" {
		return id
	}
	return bootedFromVolume
}

// listImageName is the image's name from names, upstream's marker for a
// volume-booted server, or "" when the name is unknown.
func listImageName(s servers.Server, names map[string]string) string {
	id := imageID(s.Image)
	if id == "" {
		return bootedFromVolume
	}
	return names[id]
}

// rawColumnValue renders a raw listing attribute; a map of lists (scheduler
// hints) reads as upstream's DictListColumn, "key='v1, v2'".
func rawColumnValue(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return flattenServerValue(v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		val := m[k]
		if list, ok := val.([]any); ok {
			val = formatListFlat(list)
		}
		parts = append(parts, fmt.Sprintf("%s='%s'", k, scalarString(val)))
	}
	return strings.Join(parts, ", ")
}
