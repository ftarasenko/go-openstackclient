package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// scrapeErrText renders a scrape failure for the Actual Error column without the
// URL net/http embeds in *url.Error. That URL carries the hypervisor's address,
// and this column is rendered, copied into tickets and pasted into chat — see
// AGENTS.md "Private data never leaves the org". The wrapped cause ("connection
// refused", "context deadline exceeded", "http 500") is what an operator needs.
func scrapeErrText(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		// Never fall through to uerr.Error() — it always embeds the URL, and a
		// nil cause renders as "%!s(<nil>)" rather than dropping it.
		if uerr.Err == nil {
			return "request failed"
		}
		return uerr.Err.Error()
	}
	return err.Error()
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

// neHTTPClient builds the client gatherActuals scrapes node_exporter with.
//
// The --ne-insecure transport clones http.DefaultTransport rather than
// building one from scratch: a bare &http.Transport{} loses proxy support
// (HTTPS_PROXY/NO_PROXY), the dial and TLS-handshake timeouts, and HTTP/2 —
// all of which DefaultTransport already configures sensibly. Only
// TLSClientConfig needs overriding here.
func neHTTPClient(o neOpts) *http.Client {
	hc := &http.Client{
		Timeout: time.Duration(o.timeout * float64(time.Second)),
	}
	if o.scheme == "https" && o.insecure {
		//nolint:forcetypeassert // net/http guarantees DefaultTransport is *http.Transport; same pattern as internal/auth, internal/kube, internal/vault's transport.go
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via --ne-insecure
		hc.Transport = t
	}
	return hc
}

// gatherActuals queries node_exporter on each hypervisor concurrently and fills
// the actual-usage fields on rows in place.
func gatherActuals(ctx context.Context, rows []hostRow, o neOpts) {
	hc := neHTTPClient(o)
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

			// A down host's node_exporter is unreachable; skip the scrape (and its
			// timeout) and let the renderer show "n/a (down)" rather than "err",
			// which would read as a monitoring misconfiguration.
			if isDown(*r) {
				r.actualErr = "down"
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			}

			addr := neAddress(*r, o)
			if addr == "" {
				r.actualErr = "no address"
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			}
			url := fmt.Sprintf("%s://%s:%d/metrics", o.scheme, addr, o.port)
			s1, err := scrapeNE(ctx, hc, url)
			if err != nil {
				r.actualErr = scrapeErrText(err)
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
				r.actualErr = scrapeErrText(err)
				r.cpuPhysPct, r.ramPhysPct = -1, -1
				return
			}
			r.cpuPhysPct, r.ramPhysUsedB, r.ramPhysPct = computeActual(s1, s2)
		}(&rows[i])
	}
	wg.Wait()
}
