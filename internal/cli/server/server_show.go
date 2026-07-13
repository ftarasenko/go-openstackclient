package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// showAllServerFields flattens the raw nova server object into ASCII-sorted
// Field/Value pairs, matching the breadth of `openstack server show`. Well-known
// nested attributes (addresses, flavor, security_groups, volumes_attached, …)
// are flattened OSC-style; anything else falls back to a generic dict/list
// flattening so no attribute is ever hidden.
func showAllServerFields(m map[string]any) ([]string, []any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	values := make([]any, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, k)
		values = append(values, formatServerValue(k, m[k]))
	}
	return fields, values
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
