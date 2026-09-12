package server

import (
	"fmt"
)

// Scheduler hints for "server create" (--hint).
//
// A hint is an opaque instruction to nova's scheduler: the API validates the
// handful of keys it knows (`group`, `different_host`, `same_host`, `query`,
// `target_cell`, `different_cell`, `build_near_host_ip`, `cidr`) and passes
// everything else through, because which keys mean anything depends on the
// filters the deployment enables
// (`nova/api/openstack/compute/schemas/servers.py`, `additionalProperties:
// True`). koc therefore parses the flag's shape and leaves the meaning to the
// cloud — a hint no filter reads is ignored by nova, not an error here.
//
// Hints are not microversion-gated on the request side, so this works on the
// Zed floor. (Reading them back is: nova returns `scheduler_hints` on a server
// only at 2.100, above the 2.93 cap of the oldest supported cloud — see
// listcolumns.go.)

// parseSchedulerHints turns the repeatable --hint values into nova's
// scheduler-hints object, following upstream OSC: a key given once maps to the
// string, and a key repeated maps to the list of its values in the order given
// (osc_lib.cli.parseractions.KeyValueAppendAction, collapsed in
// openstackclient/compute/v2/server.py). The distinction is load-bearing rather
// than cosmetic — nova's schema types `different_host` as "a uuid *or* an array
// of uuids", and a filter reading a hint it expects as a scalar does not want a
// one-element list.
func parseSchedulerHints(specs []string) (map[string]any, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	// Values accumulate per key, in flag order, then collapse below.
	order := make([]string, 0, len(specs))
	values := make(map[string][]string, len(specs))
	for _, spec := range specs {
		k, v, err := parseKeyVal(spec)
		if err != nil {
			return nil, fmt.Errorf("invalid --hint %q: %w", spec, err)
		}
		if _, seen := values[k]; !seen {
			order = append(order, k)
		}
		values[k] = append(values[k], v)
	}
	hints := make(map[string]any, len(order))
	for _, k := range order {
		if v := values[k]; len(v) == 1 {
			hints[k] = v[0]
		} else {
			hints[k] = v
		}
	}
	return hints, nil
}
