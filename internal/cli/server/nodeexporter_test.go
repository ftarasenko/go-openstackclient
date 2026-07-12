package server

import "testing"

const neFixture = `# HELP node_cpu_seconds_total ...
node_cpu_seconds_total{cpu="0",mode="idle"} 100
node_cpu_seconds_total{cpu="0",mode="user"} 20
node_cpu_seconds_total{cpu="1",mode="idle"} 100
node_cpu_seconds_total{cpu="1",mode="system"} 10
node_memory_MemTotal_bytes 8.0e9
node_memory_MemAvailable_bytes 2.0e9
`

func TestParseNEMetrics(t *testing.T) {
	s := parseNEMetrics(neFixture)
	if s.cpuIdle != 200 {
		t.Errorf("cpuIdle = %v, want 200", s.cpuIdle)
	}
	if s.cpuTotal != 230 {
		t.Errorf("cpuTotal = %v, want 230", s.cpuTotal)
	}
	if s.memTotal != 8e9 || s.memAvail != 2e9 {
		t.Errorf("mem = %v/%v", s.memTotal, s.memAvail)
	}
}

func TestComputeActual(t *testing.T) {
	// Between samples: idle +5, total +20 → 75% busy. Mem: used 6e9 of 8e9 = 75%.
	s1 := neSample{cpuIdle: 200, cpuTotal: 230, memTotal: 8e9, memAvail: 2e9}
	s2 := neSample{cpuIdle: 205, cpuTotal: 250, memTotal: 8e9, memAvail: 2e9}
	cpu, memUsed, memPct := computeActual(s1, s2)
	if cpu < 74.9 || cpu > 75.1 {
		t.Errorf("cpu = %v, want ~75", cpu)
	}
	if memUsed != 6e9 {
		t.Errorf("memUsed = %v, want 6e9", memUsed)
	}
	if memPct < 74.9 || memPct > 75.1 {
		t.Errorf("memPct = %v, want ~75", memPct)
	}
}

func TestComputeActual_NoDelta(t *testing.T) {
	s := neSample{cpuIdle: 1, cpuTotal: 1, memTotal: 0}
	cpu, _, memPct := computeActual(s, s)
	if cpu != -1 || memPct != -1 {
		t.Errorf("expected -1/-1 for no data, got %v/%v", cpu, memPct)
	}
}

func TestNEAddress(t *testing.T) {
	r := hostRow{name: "hv1", hostIP: "10.0.0.5"}
	if got := neAddress(r, neOpts{addressFrom: "host_ip"}); got != "10.0.0.5" {
		t.Errorf("host_ip address = %q", got)
	}
	if got := neAddress(r, neOpts{addressFrom: "name", domainSuffix: ".mgmt"}); got != "hv1.mgmt" {
		t.Errorf("name address = %q", got)
	}
	// no host_ip → falls back to name.
	if got := neAddress(hostRow{name: "hv2"}, neOpts{addressFrom: "host_ip"}); got != "hv2" {
		t.Errorf("fallback address = %q", got)
	}
}
