package server

import (
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serverColumn is one optional "server list" column: the header and how to read
// it off a listing entry.
type serverColumn struct {
	Name  string
	Value func(servers.Server) any
}

// serverListOptional are the columns "server list" renders only when
// -c/--column (or --sort-column) names one, mirroring the opt-in extras
// upstream's ListServer appends (`compute/v2/server.py`, the
// `if parsed_args.columns:` block). They are deliberately outside both the
// default and the --long listing: upstream's --long does not carry them either,
// and widening the default table would change every script that reads it
// positionally.
//
// Creation time is the one that matters most in practice — without it there is
// no way to get a server's age from a listing at all, and the fallback is one
// `server show` per server.
//
// Two of upstream's extras are absent: "Pinned Availability Zone" is nova 2.96
// and "Scheduler Hints" is 2.100, both above the 2.93 cap of the oldest cloud
// koc supports (AGENTS.md → "Minimum supported cloud"), so neither field is in
// the response koc gets. Every column below is present in /servers/detail at
// 2.1, which is what serverListMicroversion pins by default.
var serverListOptional = []serverColumn{
	{"Created At", func(s servers.Server) any { return s.Created }},
	{"Image ID", func(s servers.Server) any { return imageID(s.Image) }},
	{"Flavor ID", func(s servers.Server) any { return flavorID(s.Flavor) }},
	{"Availability Zone", func(s servers.Server) any { return s.AvailabilityZone }},
	{"Host", func(s servers.Server) any { return s.Host }},
	{"Task State", func(s servers.Server) any { return s.TaskState }},
	{"Power State", func(s servers.Server) any { return s.PowerState }},
	{"Project ID", func(s servers.Server) any { return s.TenantID }},
	{"User ID", func(s servers.Server) any { return s.UserID }},
	{"Security Groups", func(s servers.Server) any { return securityGroupNames(s.SecurityGroups) }},
	{"Properties", func(s servers.Server) any { return formatServerMetadata(s.Metadata) }},
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
