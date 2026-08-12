package server

import (
	"net/http"
	"testing"
)

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

// The --ne-insecure transport must still be a clone of http.DefaultTransport
// (proxy support, dial/handshake timeouts, HTTP/2), not a bare &http.Transport{}
// that only sets TLSClientConfig and silently drops all of that.
func TestNEHTTPClient_InsecureTransportClonesDefault(t *testing.T) {
	hc := neHTTPClient(neOpts{scheme: "https", insecure: true, timeout: 5})
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", hc.Transport)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify not set on the transport")
	}
	if tr.Proxy == nil {
		t.Error("Proxy is nil: transport was built from scratch instead of cloning http.DefaultTransport, losing HTTPS_PROXY/NO_PROXY support")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout is 0: transport did not inherit http.DefaultTransport's timeouts")
	}
}

// Without --ne-insecure (or on http), the client keeps its default transport —
// no need for a custom one at all.
func TestNEHTTPClient_DefaultsToStdlibTransport(t *testing.T) {
	hc := neHTTPClient(neOpts{scheme: "http", insecure: false, timeout: 5})
	if hc.Transport != nil {
		t.Errorf("Transport = %v, want nil (net/http.Client uses DefaultTransport)", hc.Transport)
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
