package server

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
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

// buildSchedulerHints turns the hint flags into what servers.Create takes as its
// fourth argument — hints travel beside the server object, not inside it, which
// is why they are an argument of their own rather than a CreateOpts field.
//
// Every hint goes in as an additional property rather than onto
// SchedulerHintOpts' typed fields: those validate client-side in ways nova does
// not — Query must be a parsed statement of at least three elements,
// BuildNearHostIP must carry a CIDR suffix it then splits in two — so routing a
// free-text flag through them would reject values upstream OSC sends verbatim.
// Nova is the validator; koc checks the flag's shape only.
//
// A nil builder is the "no hints were asked for" answer, and is what keeps the
// key out of the request body altogether.
func buildSchedulerHints(ctx context.Context, client *gophercloud.ServiceClient,
	f *serverCreateFlags,
) (servers.SchedulerHintOptsBuilder, error) {
	hints, err := parseSchedulerHints(f.hints)
	if err != nil {
		return nil, err
	}
	// --server-group is upstream's alias for --hint group=<id>, with the name
	// lookup done for you. Given both, it wins — the same precedence upstream
	// has, since it assigns hints['group'] after collapsing the --hint values
	// (openstackclient/compute/v2/server.py CreateServer.take_action).
	if f.serverGroup != "" {
		groupID, err := resolveServerGroupID(ctx, client, f.serverGroup)
		if err != nil {
			return nil, err
		}
		if hints == nil {
			hints = make(map[string]any, 1)
		}
		hints["group"] = groupID
	}
	if len(hints) == 0 {
		return nil, nil
	}
	return servers.SchedulerHintOpts{AdditionalProperties: hints}, nil
}
