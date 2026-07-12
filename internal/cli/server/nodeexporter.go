package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// neOpts configures the optional node_exporter actual-usage comparison.
type neOpts struct {
	port           int
	scheme         string // http | https
	addressFrom    string // host_ip | name
	domainSuffix   string
	timeout        float64
	sampleInterval float64
	concurrency    int
	insecure       bool
}

// neSample is a single node_exporter scrape reduced to the values we need.
type neSample struct {
	cpuIdle  float64
	cpuTotal float64
	memTotal float64
	memAvail float64
}

var metricRe = regexp.MustCompile(`^(?P<name>[a-zA-Z_:][a-zA-Z0-9_:]*)(?P<labels>\{[^}]*\})?\s+(?P<value>[-+0-9.eE]+|NaN|[-+]Inf)\s*$`)

// parseNEMetrics reduces a /metrics body to a neSample.
func parseNEMetrics(text string) neSample {
	var s neSample
	for _, line := range strings.Split(text, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		m := metricRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, labels, valStr := m[1], m[2], m[3]
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		switch name {
		case "node_cpu_seconds_total":
			s.cpuTotal += val
			if strings.Contains(labels, `mode="idle"`) {
				s.cpuIdle += val
			}
		case "node_memory_MemTotal_bytes":
			s.memTotal = val
		case "node_memory_MemAvailable_bytes":
			s.memAvail = val
		}
	}
	return s
}

// computeActual derives CPU utilization (%), memory used (bytes) and memory
// utilization (%) from two samples. cpuPct/memPct are -1 when unknown.
func computeActual(s1, s2 neSample) (cpuPct, memUsedB, memPct float64) {
	cpuPct, memPct = -1, -1
	if dTotal := s2.cpuTotal - s1.cpuTotal; dTotal > 0 {
		cpuPct = 100 * (1 - (s2.cpuIdle-s1.cpuIdle)/dTotal)
	}
	memUsedB = s2.memTotal - s2.memAvail
	if s2.memTotal > 0 {
		memPct = 100 * memUsedB / s2.memTotal
	}
	return cpuPct, memUsedB, memPct
}

// neAddress builds the host:port target for a hypervisor.
func neAddress(r hostRow, o neOpts) string {
	host := r.hostIP
	if o.addressFrom == "name" || host == "" {
		host = r.name
		if o.domainSuffix != "" {
			host += o.domainSuffix
		}
	}
	return host
}

func scrapeNE(ctx context.Context, hc *http.Client, url string) (neSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return neSample{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return neSample{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return neSample{}, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return neSample{}, err
	}
	return parseNEMetrics(string(body)), nil
}

// gatherActuals queries node_exporter on each hypervisor concurrently and fills
// the actual-usage fields on rows in place.
func gatherActuals(ctx context.Context, rows []hostRow, o neOpts) {
	hc := &http.Client{
		Timeout: time.Duration(o.timeout * float64(time.Second)),
	}
	if o.scheme == "https" && o.insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in via --ne-insecure
	}
	interval := time.Duration(o.sampleInterval * float64(time.Second))

	conc := o.concurrency
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for i := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *hostRow) {
			defer wg.Done()
			defer func() { <-sem }()

			addr := neAddress(*r, o)
			if addr == "" {
				r.actualErr = "no address"
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			}
			url := fmt.Sprintf("%s://%s:%d/metrics", o.scheme, addr, o.port)
			s1, err := scrapeNE(ctx, hc, url)
			if err != nil {
				r.actualErr = err.Error()
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			}
			select {
			case <-ctx.Done():
				r.actualErr = "cancelled"
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			case <-time.After(interval):
			}
			s2, err := scrapeNE(ctx, hc, url)
			if err != nil {
				r.actualErr = err.Error()
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			}
			r.cpuPhysPct, r.ramPhysUsedB, r.ramPhysPct = computeActual(s1, s2)
		}(&rows[i])
	}
	wg.Wait()
}
