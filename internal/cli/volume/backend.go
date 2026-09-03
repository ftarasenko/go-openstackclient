package volume

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "koc volume backend pool list" and "koc volume backend capability show" —
// cinder's scheduler view of its storage backends, mirroring upstream OSC's
// `volume backend pool list` / `volume backend capability show`.
//
// Both are raw ServiceClient calls (an AGENTS.md-sanctioned fallback) decoding
// into map[string]any. gophercloud does ship blockstorage/v3/schedulerstats,
// but its Capabilities struct models six keys — driver_version, storage_protocol,
// total/free_capacity_gb, vendor_name, volume_backend_name — and silently drops
// every other one the driver reported, including allocated_capacity_gb,
// backend_state and the whole vendor-specific set. Those are exactly the figures
// a capacity decision turns on, so a typed decode would throw away the answer.
// /capabilities/{host} has no gophercloud package at all.

// poolCoreCapabilities are the capability keys the default pool listing renders,
// in order. Everything else the driver reported is added by --long.
//
// Cinder normalises these across drivers, and a replicated backend is where the
// normalisation leaks: the scheduler is handed one total and one free figure per
// pool, and a driver that computes total from the cluster's raw capacity while
// reporting free after replication produces a pair that cannot both be right.
// koc reports what cinder reports and does not try to reconcile it; --long shows
// the driver's own keys next to it, which is where the raw and the replicated
// figure can be compared.
var poolCoreCapabilities = []string{
	"backend_state",
	"total_capacity_gb",
	"free_capacity_gb",
	"allocated_capacity_gb",
	"provisioned_capacity_gb",
	"volume_backend_name",
}

// newBackendCommand builds the "volume backend" parent group.
func newBackendCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "backend", Short: "Block-storage backend pools and capabilities"}
	pool := &cobra.Command{Use: "pool", Short: "Scheduler storage pools"}
	pool.AddCommand(newBackendPoolListCommand(a, o))
	capability := &cobra.Command{Use: "capability", Short: "Backend driver capabilities"}
	capability.AddCommand(newBackendCapabilityShowCommand(a, o))
	cmd.AddCommand(pool, capability)
	return cmd
}

func newBackendPoolListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cinder scheduler storage pools and their capacity (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runBackendPoolList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	// Upstream OSC's pool listing shows the pool name and nothing else without
	// --long, which cannot answer "does this pool have room". koc shows the
	// capacity figures by default and keeps --long for the driver's full
	// capability set — the deviation is recorded in docs/coverage.md.
	cmd.Flags().BoolVar(&long, "long", false, "list every capability the driver reports, not just capacity")
	return cmd
}

// storagePool is one entry of GET /scheduler-stats/get_pools?detail=True.
type storagePool struct {
	Name         string         `json:"name"`
	Capabilities map[string]any `json:"capabilities"`
}

func runBackendPoolList(ctx context.Context, client *gophercloud.ServiceClient,
	o *output.Options, long bool, w io.Writer,
) error {
	var resp struct {
		Pools []storagePool `json:"pools"`
	}
	// detail=True is what makes cinder return the capabilities object; without it
	// the response is a list of bare names.
	u := client.ServiceURL("scheduler-stats", "get_pools") + "?detail=True"
	r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("listing storage pools: %w", err)
	}
	return o.WriteList(w, poolTable(resp.Pools, long))
}

// poolTable renders the pool listing. The capability keys are unioned across
// pools rather than read off the first one: cinder returns whatever each driver
// reported, so two backends in the same cloud do not agree on the key set.
func poolTable(pools []storagePool, long bool) output.Table {
	keys := slices.Clone(poolCoreCapabilities)
	if long {
		keys = append(keys, extraCapabilityKeys(pools)...)
	}
	cols := make([]string, 0, len(keys)+1)
	cols = append(cols, "Name")
	for _, k := range keys {
		cols = append(cols, capabilityHeader(k))
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(pools))}
	for _, p := range pools {
		row := make([]any, 0, len(keys)+1)
		row = append(row, p.Name)
		for _, k := range keys {
			row = append(row, capabilityCell(p.Capabilities[k]))
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// extraCapabilityKeys is every capability key any pool reported that the core
// set does not already cover, ASCII-sorted so the column order is stable.
func extraCapabilityKeys(pools []storagePool) []string {
	seen := map[string]bool{}
	for _, p := range pools {
		for k := range p.Capabilities {
			if !slices.Contains(poolCoreCapabilities, k) {
				seen[k] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// capabilityCell renders one capability value.
//
// Scalars are passed through untouched, deliberately: cinder reports a capacity
// as a number for most drivers but as the string "infinite" or "unknown" for a
// thin-provisioned or a not-yet-reporting one, and rewriting either into the
// other would invent a figure. Composites (replication_targets, a nested
// properties object) are rendered as compact JSON, since a table cell has to be
// one line.
func capabilityCell(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case map[string]any, []any:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	default:
		return t
	}
}

// capabilityAcronyms are the capability-key words that read wrong in title case.
var capabilityAcronyms = map[string]string{
	"gb": "GB", "id": "ID", "ip": "IP", "iops": "IOPS",
	"qos": "QoS", "url": "URL", "uuid": "UUID", "vg": "VG",
}

// capabilityHeader turns a snake_case capability key into a column heading, so
// total_capacity_gb reads as "Total Capacity GB".
func capabilityHeader(key string) string {
	words := strings.Split(key, "_")
	for i, word := range words {
		if word == "" {
			continue
		}
		if fixed, ok := capabilityAcronyms[word]; ok {
			words[i] = fixed
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func newBackendCapabilityShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <host>",
		Short: "Show the capabilities a backend's driver reports (admin)",
		Long: "Show the capabilities a backend's driver reports (admin).\n\n" +
			"<host> is the cinder service host, optionally with its backend — \"<host>@<backend>\" — " +
			"as it appears in the Host column of \"koc volume service list\" or in a volume's " +
			"os-vol-host-attr:host. The driver's own capacity keys are reported verbatim, which is " +
			"where a replicated backend's raw and post-replication figures can be told apart.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runBackendCapabilityShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runBackendCapabilityShow(ctx context.Context, client *gophercloud.ServiceClient,
	o *output.Options, host string, w io.Writer,
) error {
	var resp map[string]any
	r, err := client.Get(ctx, client.ServiceURL("capabilities", host), &resp,
		&gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("showing capabilities of backend %q: %w", host, err)
	}
	// json/yaml keep the raw structured values so they can be parsed; the text
	// views get one line per field, as everywhere else in koc.
	raw := o.Format == output.FormatJSON || o.Format == output.FormatYAML
	fields := make([]string, 0, len(resp))
	for k := range resp {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	values := make([]any, 0, len(fields))
	for _, k := range fields {
		if raw {
			values = append(values, resp[k])
			continue
		}
		values = append(values, capabilityCell(resp[k]))
	}
	return o.WriteSingle(w, fields, values)
}
