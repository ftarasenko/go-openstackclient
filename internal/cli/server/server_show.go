package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// serverFieldAliases renames the raw nova attributes that `openstack server
// show` presents under a friendlier name, so koc matches OSC's field set in
// every format.
var serverFieldAliases = map[string]string{
	"tenant_id":                            "project_id",
	"metadata":                             "properties",
	"os-extended-volumes:volumes_attached": "volumes_attached",
}

// showServerFields turns the raw nova server object into ASCII-sorted
// Field/Value pairs matching `openstack server show`: attributes are renamed to
// OSC's names, the redundant "links" attribute is dropped, and volume-boot /
// power-state values are humanized. When flatten is true (table/csv/value)
// nested attributes (addresses, flavor, security_groups, volumes_attached, …)
// are collapsed to OSC-style strings; when false (json/yaml) the raw structured
// values are preserved so consumers can parse them, mirroring OSC.
func showServerFields(m map[string]any, flatten bool) ([]string, []any) {
	renamed := make(map[string]any, len(m))
	for k, v := range m {
		if k == "links" {
			continue // OSC omits the self/bookmark links
		}
		if alias, ok := serverFieldAliases[k]; ok {
			k = alias
		}
		renamed[k] = v
	}
	keys := make([]string, 0, len(renamed))
	for k := range renamed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	values := make([]any, 0, len(keys))
	for _, k := range keys {
		v := humanizeServerValue(k, renamed[k], flatten)
		if flatten {
			v = formatServerValue(k, v)
		}
		fields = append(fields, k)
		values = append(values, v)
	}
	return fields, values
}

// powerStates maps nova's numeric OS-EXT-STS:power_state to OSC's label.
var powerStates = map[int]string{
	0: "NOSTATE", 1: "Running", 3: "Paused", 4: "Shutdown", 6: "Crashed", 7: "Suspended",
}

// humanizeServerValue applies OSC's display substitutions. "image" is replaced
// with "N/A (booted from volume)" when empty in every format (OSC does so even
// in JSON); "power_state" is turned into its label only for the flattened
// (table/csv/value) view, since OSC keeps the raw integer in json/yaml.
func humanizeServerValue(key string, v any, flatten bool) any {
	switch {
	case key == "image":
		return serverImage(v)
	case key == "OS-EXT-STS:power_state" && flatten:
		return powerStateLabel(v)
	}
	return v
}

func serverImage(v any) any {
	switch t := v.(type) {
	case nil:
		return "N/A (booted from volume)"
	case string:
		if t == "" {
			return "N/A (booted from volume)"
		}
	case map[string]any:
		if len(t) == 0 {
			return "N/A (booted from volume)"
		}
	}
	return v
}

func powerStateLabel(v any) any {
	var n int
	switch t := v.(type) {
	case float64:
		n = int(t)
	case int:
		n = t
	default:
		return v
	}
	if s, ok := powerStates[n]; ok {
		return s
	}
	return v
}

func formatServerValue(key string, v any) any {
	if key == "addresses" {
		return formatServerAddresses(v)
	}
	return flattenServerValue(v)
}

func flattenServerValue(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case map[string]any:
		return formatDictFlat(t)
	case []any:
		return formatListFlat(t)
	default:
		return t
	}
}

// formatDictFlat renders a map as "k='v', k2='v2'" with keys ASCII-sorted and
// nested maps flattened with a dot (matching OSC's flavor extra_specs display).
func formatDictFlat(m map[string]any) string {
	flat := map[string]string{}
	flattenDict("", m, flat)
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s='%s'", k, flat[k]))
	}
	return strings.Join(parts, ", ")
}

func flattenDict(prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flattenDict(key, sub, out)
			continue
		}
		out[key] = scalarString(v)
	}
}

// formatListFlat renders a list of dicts one-per-line (OSC style for
// volumes_attached / security_groups) and a list of scalars as a sorted,
// comma-joined string.
func formatListFlat(list []any) string {
	if len(list) == 0 {
		return "[]"
	}
	allMaps := true
	for _, e := range list {
		if _, ok := e.(map[string]any); !ok {
			allMaps = false
			break
		}
	}
	if allMaps {
		parts := make([]string, 0, len(list))
		for _, e := range list {
			parts = append(parts, formatDictFlat(e.(map[string]any)))
		}
		return strings.Join(parts, "\n")
	}
	strs := make([]string, 0, len(list))
	for _, e := range list {
		strs = append(strs, scalarString(e))
	}
	sort.Strings(strs)
	return strings.Join(strs, ", ")
}

// formatServerAddresses renders the nova addresses map as
// "network=ip1, ip2; network2=ip3".
func formatServerAddresses(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return flattenServerValue(v).(string)
	}
	nets := make([]string, 0, len(m))
	for k := range m {
		nets = append(nets, k)
	}
	sort.Strings(nets)
	parts := make([]string, 0, len(nets))
	for _, n := range nets {
		list, _ := m[n].([]any)
		ips := make([]string, 0, len(list))
		for _, e := range list {
			if am, ok := e.(map[string]any); ok {
				if addr, ok := am["addr"].(string); ok {
					ips = append(ips, addr)
				}
			}
		}
		parts = append(parts, fmt.Sprintf("%s=%s", n, strings.Join(ips, ", ")))
	}
	return strings.Join(parts, "; ")
}

// scalarString renders a JSON scalar the way OSC would: numbers without a
// trailing ".0", nil as empty, and any residual composite as compact JSON.
func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
