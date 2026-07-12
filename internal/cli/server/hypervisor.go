package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/hypervisors"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newHypervisorCommand builds the "hypervisor" command group.
func newHypervisorCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hypervisor",
		Short: "Compute hypervisor commands",
	}
	cmd.AddCommand(newHypervisorListCommand(a, o))
	return cmd
}

func newHypervisorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	g := &gaugeOpts{}
	var gauge bool
	var colorMode string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List hypervisors",
		Long: "List hypervisors. With --gauge, renders allocation gauges with " +
			"threshold colors, auto-fitting the terminal width; --check-actual adds " +
			"real CPU/RAM usage scraped from node_exporter. A Health column surfaces " +
			"down and disabled hosts (down rows are dimmed and their stale gauges " +
			"suppressed), with a fleet up/down/disabled tally beneath the table.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			switch colorMode {
			case "always":
				t := true
				g.color = &t
			case "never":
				f := false
				g.color = &f
			}
			ctx := cmd.Context()
			compute, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			if !gauge {
				return runHypervisorList(ctx, compute, o, cmd.OutOrStdout())
			}
			return runHypervisorGauge(ctx, compute, session, o, g, cmd.OutOrStdout())
		},
	}

	fs := cmd.Flags()
	fs.BoolVar(&gauge, "gauge", false, "render allocation gauges with color, width-aware")
	fs.IntVar(&g.barWidth, "bar-width", 0, "gauge width in characters (0 = auto by terminal)")
	fs.BoolVar(&g.ascii, "ascii", false, "use ASCII bars instead of Unicode blocks")
	fs.StringVar(&colorMode, "color", "auto", "color output: auto, always or never")
	fs.Float64Var(&g.warnPct, "warn-pct", 70, "RAM/Disk warning threshold percent")
	fs.Float64Var(&g.critPct, "crit-pct", 90, "RAM/Disk critical threshold percent")
	fs.BoolVar(&g.includeIronic, "include-ironic", false, "include ironic hypervisors")
	fs.StringVar(&g.aggregate, "aggregate", "", "only show hypervisors in this aggregate")
	fs.StringVar(&g.sortKey, "sort", "name", "sort column: name, aggregate, vms, overcommit, ram, disk")
	fs.BoolVar(&g.reverse, "reverse", false, "reverse sort order")
	fs.IntVar(&g.width, "width", 0, "override detected terminal width")

	fs.BoolVar(&g.checkActual, "check-actual", false, "compare real CPU/RAM usage from node_exporter")
	fs.IntVar(&g.ne.port, "ne-port", 9100, "node_exporter port")
	fs.StringVar(&g.ne.scheme, "ne-scheme", "http", "node_exporter scheme: http or https")
	fs.StringVar(&g.ne.addressFrom, "ne-address-from", "host_ip", "reach a hypervisor by host_ip or name")
	fs.StringVar(&g.ne.domainSuffix, "ne-domain-suffix", "", "suffix appended to the host name when building the URL")
	fs.Float64Var(&g.ne.timeout, "ne-timeout", 5, "per-request timeout seconds")
	fs.Float64Var(&g.ne.sampleInterval, "ne-sample-interval", 1.0, "seconds between the two CPU samples")
	fs.IntVar(&g.ne.concurrency, "ne-concurrency", 8, "parallel node_exporter queries")
	fs.BoolVar(&g.ne.insecure, "ne-insecure", false, "disable TLS verification for https node_exporter")
	return cmd
}

// runHypervisorList is the plain, OSC-compatible listing.
func runHypervisorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := hypervisors.List(client, nil).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing hypervisors: %w", err)
	}
	all, err := hypervisors.ExtractHypervisors(pages)
	if err != nil {
		return fmt.Errorf("parsing hypervisor list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Hypervisor Hostname", "Type", "State", "Status"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, h := range all {
		t.Rows = append(t.Rows, []any{h.ID, h.HypervisorHostname, h.HypervisorType, h.State, h.Status})
	}
	return o.WriteList(w, t)
}

// runHypervisorGauge gathers enriched rows and renders them: gauges for the
// table format, raw numbers through the output layer for json/csv/yaml/value.
func runHypervisorGauge(ctx context.Context, compute *gophercloud.ServiceClient, session *auth.Client, o *output.Options, g *gaugeOpts, w io.Writer) error {
	rows, err := gatherHypervisorRows(ctx, compute, g)
	if err != nil {
		return err
	}
	// vCPU/RAM/Disk allocation lives in placement (nova dropped these fields at
	// microversion 2.88). Enrich from placement, which is the authoritative
	// source; nova stays the source for hostname/type/state/VMs/cpu_model/host_ip.
	if pc, perr := session.Placement(); perr == nil {
		enrichFromPlacement(ctx, pc, rows)
	}
	if g.checkActual && len(rows) > 0 {
		gatherActuals(ctx, rows, g.ne)
	}
	sortRows(rows, g.sortKey, g.reverse)

	if o.Format == output.FormatTable {
		renderGauge(w, rows, g)
		return nil
	}
	return o.WriteList(w, gaugeRawTable(rows, g.checkActual))
}

func gatherHypervisorRows(ctx context.Context, client *gophercloud.ServiceClient, g *gaugeOpts) ([]hostRow, error) {
	// Nova microversion 2.88 REMOVED the hypervisor usage fields (vcpus,
	// memory_mb, local_gb, running_vms, cpu_info, host_ip, …) — they moved to
	// placement — so the negotiated "latest" returns them as 0. Fetch the gauge
	// data with the default microversion (2.1), which still includes every field
	// and is supported by every nova.
	hvClient := *client
	hvClient.Microversion = ""

	pages, err := hypervisors.List(&hvClient, nil).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing hypervisors: %w", err)
	}
	all, err := hypervisors.ExtractHypervisors(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing hypervisor list: %w", err)
	}

	hostAggr := map[string][]string{}
	if apages, aerr := aggregates.List(client).AllPages(ctx); aerr == nil {
		if aggs, xerr := aggregates.ExtractAggregates(apages); xerr == nil {
			for _, ag := range aggs {
				for _, h := range ag.Hosts {
					hostAggr[h] = append(hostAggr[h], ag.Name)
				}
			}
		}
	}

	rows := make([]hostRow, 0, len(all))
	for _, h := range all {
		if !g.includeIronic && strings.EqualFold(h.HypervisorType, "ironic") {
			continue
		}
		name := h.HypervisorHostname
		aggrs := hostAggr[name]
		if g.aggregate != "" && !contains(aggrs, g.aggregate) {
			continue
		}
		oc := ratio(float64(h.VCPUsUsed), float64(h.VCPUs))
		ramUsed, ramTotal := float64(h.MemoryMBUsed), float64(h.MemoryMB)
		diskUsed, diskTot := float64(h.LocalGBUsed), float64(h.LocalGB)
		rows = append(rows, hostRow{
			name:        name,
			aggregate:   strings.Join(aggrs, ","),
			htype:       h.HypervisorType,
			vms:         h.RunningVMs,
			vcpusUsed:   h.VCPUsUsed,
			vcpusTotal:  h.VCPUs,
			overcommit:  oc,
			ramUsedMB:   ramUsed,
			ramTotalMB:  ramTotal,
			ramPct:      100 * ratio(ramUsed, ramTotal),
			diskUsedGB:  diskUsed,
			diskTotGB:   diskTot,
			diskPct:     100 * ratio(diskUsed, diskTot),
			cpuModel:    h.CPUInfo.Model,
			state:       h.State,
			status:      h.Status,
			hostIP:      h.HostIP,
			cpuAllocPct: 100 * oc,
			cpuPhysPct:  -1,
			ramPhysPct:  -1,
		})
	}
	return rows, nil
}

// gaugeRawTable renders rows as plain columns (no gauges/color) for
// json/csv/yaml/value output.
func gaugeRawTable(rows []hostRow, checkActual bool) output.Table {
	cols := []string{
		"Name", "Aggregate", "Type", "VMs", "vCPUs Used", "vCPUs Total", "Overcommit",
		"RAM %", "RAM Used GiB", "RAM Total GiB", "Disk %", "Disk Used GiB", "Disk Total GiB",
		"CPU Model", "State", "Status",
	}
	if checkActual {
		cols = append(cols, "CPU Alloc %", "CPU Phys %", "RAM Phys %", "RAM Phys Used GiB", "Actual Error")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(rows))}
	for _, r := range rows {
		row := []any{
			r.name, r.aggregate, r.htype, r.vms, r.vcpusUsed, r.vcpusTotal, round2(r.overcommit),
			round2(r.ramPct), round2(r.ramUsedMB / 1024), round2(r.ramTotalMB / 1024),
			round2(r.diskPct), r.diskUsedGB, r.diskTotGB, r.cpuModel, r.state, r.status,
		}
		if checkActual {
			row = append(row, round2(r.cpuAllocPct), round2(r.cpuPhysPct), round2(r.ramPhysPct),
				round2(r.ramPhysUsedB/(1024*1024*1024)), r.actualErr)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// enrichFromPlacement overrides each row's vCPU/RAM/Disk totals and usage from
// placement, matching a resource provider to the hypervisor by name. It is
// best-effort: on any error the nova-sourced values are left in place. A
// resource class is only overridden when the RP actually has that inventory
// (e.g. DISK_GB may live on a shared-storage provider), so nothing is zeroed.
func enrichFromPlacement(ctx context.Context, pc *gophercloud.ServiceClient, rows []hostRow) {
	pages, err := resourceproviders.List(pc, resourceproviders.ListOpts{}).AllPages(ctx)
	if err != nil {
		return
	}
	rps, err := resourceproviders.ExtractResourceProviders(pages)
	if err != nil {
		return
	}
	nameToUUID := make(map[string]string, len(rps))
	for _, rp := range rps {
		nameToUUID[rp.Name] = rp.UUID
	}

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range rows {
		uuid, ok := nameToUUID[rows[i].name]
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(r *hostRow, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			inv, err := resourceproviders.GetInventories(ctx, pc, id).Extract()
			if err != nil {
				return
			}
			use, err := resourceproviders.GetUsages(ctx, pc, id).Extract()
			if err != nil {
				return
			}
			applyPlacement(r, inv, use)
		}(&rows[i], uuid)
	}
	wg.Wait()
}

func applyPlacement(r *hostRow, inv *resourceproviders.ResourceProviderInventories, use *resourceproviders.ResourceProviderUsage) {
	if v, ok := inv.Inventories["VCPU"]; ok {
		r.vcpusTotal = v.Total
		r.vcpusUsed = use.Usages["VCPU"]
		r.overcommit = ratio(float64(r.vcpusUsed), float64(r.vcpusTotal))
		r.cpuAllocPct = 100 * r.overcommit
	}
	if v, ok := inv.Inventories["MEMORY_MB"]; ok {
		r.ramTotalMB = float64(v.Total)
		r.ramUsedMB = float64(use.Usages["MEMORY_MB"])
		r.ramPct = 100 * ratio(r.ramUsedMB, r.ramTotalMB)
	}
	if v, ok := inv.Inventories["DISK_GB"]; ok {
		r.diskTotGB = float64(v.Total)
		r.diskUsedGB = float64(use.Usages["DISK_GB"])
		r.diskPct = 100 * ratio(r.diskUsedGB, r.diskTotGB)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
