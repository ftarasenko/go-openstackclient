package server

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
)

func TestApplyPlacement(t *testing.T) {
	r := hostRow{}
	inv := &resourceproviders.ResourceProviderInventories{
		Inventories: map[string]resourceproviders.Inventory{
			"VCPU":      {Total: 96},
			"MEMORY_MB": {Total: 512 * 1024},
			"DISK_GB":   {Total: 3666},
		},
	}
	use := &resourceproviders.ResourceProviderUsage{
		Usages: map[string]int{"VCPU": 384, "MEMORY_MB": 256 * 1024, "DISK_GB": 1800},
	}
	applyPlacement(&r, inv, use)

	if r.vcpusTotal != 96 || r.vcpusUsed != 384 {
		t.Errorf("vcpu = %d/%d, want 384/96", r.vcpusUsed, r.vcpusTotal)
	}
	if r.overcommit < 3.99 || r.overcommit > 4.01 {
		t.Errorf("overcommit = %v, want ~4.0", r.overcommit)
	}
	if r.ramTotalMB != 512*1024 || r.ramPct != 50 {
		t.Errorf("ram = %v%% of %v", r.ramPct, r.ramTotalMB)
	}
	if r.diskTotGB != 3666 || r.diskUsedGB != 1800 {
		t.Errorf("disk = %v/%v", r.diskUsedGB, r.diskTotGB)
	}
}

func TestApplyPlacement_MissingClassKept(t *testing.T) {
	r := hostRow{diskTotGB: 419, diskUsedGB: 0} // nova-sourced disk, no DISK_GB in placement
	inv := &resourceproviders.ResourceProviderInventories{
		Inventories: map[string]resourceproviders.Inventory{"VCPU": {Total: 88}},
	}
	use := &resourceproviders.ResourceProviderUsage{Usages: map[string]int{"VCPU": 100}}
	applyPlacement(&r, inv, use)
	if r.diskTotGB != 419 {
		t.Errorf("disk total should be kept when placement lacks DISK_GB, got %v", r.diskTotGB)
	}
}

func sampleRows() []hostRow {
	return []hostRow{
		{name: "hv-a", aggregate: "rack1", htype: "QEMU", vms: 12, vcpusUsed: 96, vcpusTotal: 128,
			overcommit: 0.75, ramUsedMB: 266 * 1024, ramTotalMB: 512 * 1024, ramPct: 52,
			diskUsedGB: 1200, diskTotGB: 6000, diskPct: 20, cpuModel: "Cascadelake", state: "up", status: "enabled",
			cpuAllocPct: 75, cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-b", aggregate: "rack1", htype: "QEMU", vms: 8, vcpusUsed: 64, vcpusTotal: 128,
			overcommit: 0.5, ramUsedMB: 480 * 1024, ramTotalMB: 512 * 1024, ramPct: 94,
			diskUsedGB: 900, diskTotGB: 6000, diskPct: 15, cpuModel: "Cascadelake", state: "up", status: "enabled",
			cpuAllocPct: 50, cpuPhysPct: -1, ramPhysPct: -1},
	}
}

func TestPickProfileByWidth(t *testing.T) {
	cases := []struct {
		w      int
		actual bool
		want   profile
	}{
		{80, false, profileCompact},
		{160, false, profileWide},
		{240, false, profileFull},
		{80, true, profileFull}, // --check-actual forces full
	}
	for _, c := range cases {
		if got := pickProfile(c.w, c.actual); got != c.want {
			t.Errorf("pickProfile(%d,%v) = %d, want %d", c.w, c.actual, got, c.want)
		}
	}
}

func TestBarFillAndFormat(t *testing.T) {
	// 50% of width 8 → 4 filled, 4 empty; no color.
	got := bar(50, 8, false, false, 70, 90)
	if got != "[████    ] 50%" {
		t.Errorf("bar = %q", got)
	}
	// ASCII mode, clamps >100.
	if a := bar(150, 4, true, false, 70, 90); a != "[####] 100%" {
		t.Errorf("ascii bar = %q", a)
	}
}

func TestVisLenIgnoresANSI(t *testing.T) {
	colored := colorize("abc", 31, true)
	if visLen(colored) != 3 {
		t.Errorf("visLen(%q) = %d, want 3", colored, visLen(colored))
	}
}

func TestRenderGauge_CompactVsFull(t *testing.T) {
	o := &gaugeOpts{width: 80, warnPct: 70, critPct: 90} // compact, color off (not a TTY)
	var buf bytes.Buffer
	renderGauge(&buf, sampleRows(), o)
	out := buf.String()
	// compact profile: has Name/VMs/OC/RAM/Disk/St headers, but NOT wide-only cols.
	if !strings.Contains(out, "Name") || !strings.Contains(out, "RAM") || !strings.Contains(out, "Disk") {
		t.Errorf("compact missing core columns:\n%s", out)
	}
	if strings.Contains(out, "CPU Model") || strings.Contains(out, "Aggregate") {
		t.Errorf("compact should not include wide columns:\n%s", out)
	}
	if !strings.Contains(out, "hv-a") || !strings.Contains(out, "hv-b") {
		t.Errorf("rows missing:\n%s", out)
	}

	o.width = 240
	buf.Reset()
	renderGauge(&buf, sampleRows(), o)
	full := buf.String()
	if !strings.Contains(full, "Aggregate") || !strings.Contains(full, "CPU Model") || !strings.Contains(full, "vCPU(u/t)") {
		t.Errorf("full profile missing wide columns:\n%s", full)
	}
}

func TestRenderGauge_ColorAndThreshold(t *testing.T) {
	on := true
	o := &gaugeOpts{width: 80, warnPct: 70, critPct: 90, color: &on}
	var buf bytes.Buffer
	renderGauge(&buf, sampleRows(), o)
	out := buf.String()
	// hv-b RAM is 94% → red (\x1b[31m) should appear.
	if !strings.Contains(out, "\x1b[31m") {
		t.Errorf("expected red for RAM >= crit:\n%q", out)
	}
	// green (32) for the low-usage disk.
	if !strings.Contains(out, "\x1b[32m") {
		t.Errorf("expected green for low usage:\n%q", out)
	}
}
