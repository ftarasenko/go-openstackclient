package auth

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedClock makes each round trip appear to take a known duration, so the
// rendered line is deterministic.
func fixedClock(t *testing.T, step time.Duration) {
	t.Helper()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	calls := 0
	old := timeNow
	timeNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now := base.Add(time.Duration(calls) * step)
		calls++
		return now
	}
	t.Cleanup(func() { timeNow = old })
}

func TestTimingTransport_LogsMethodURLStatusAndDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fixedClock(t, 250*time.Millisecond)

	var log bytes.Buffer
	client := &http.Client{Transport: newTimingTransport(http.DefaultTransport, &log)}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/servers/detail", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	line := log.String()
	for _, want := range []string{"timing:", "GET", "/servers/detail", "200", "250ms"} {
		if !strings.Contains(line, want) {
			t.Errorf("timing line %q missing %q", line, want)
		}
	}
}

// A URL carrying credentials in userinfo must not be logged verbatim.
func TestTimingTransport_RedactsURLUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fixedClock(t, time.Millisecond)

	var log bytes.Buffer
	client := &http.Client{Transport: newTimingTransport(http.DefaultTransport, &log)}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/nodes", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.URL.User = url.UserPassword("ironic", "s3cret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if strings.Contains(log.String(), "s3cret") {
		t.Errorf("timing line leaked the URL password: %q", log.String())
	}
	if !strings.Contains(log.String(), "xxxxx") {
		t.Errorf("timing line %q should carry the redaction marker", log.String())
	}
}

// A transport error still produces a line — a call that failed after 30 seconds is
// exactly what --timing is for.
func TestTimingTransport_LogsFailedRoundTrips(t *testing.T) {
	fixedClock(t, 2*time.Second)

	var log bytes.Buffer
	client := &http.Client{Transport: newTimingTransport(errorRoundTripper{}, &log)}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.invalid/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the round trip to fail")
	}
	for _, want := range []string{"POST", "error", "2s"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("timing line %q missing %q", log.String(), want)
		}
	}
}

type errorRoundTripper struct{}

func (errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrHandlerTimeout
}

// Upstream's timing report ends with a Total row; koc keeps its per-request
// lines on stderr (see the timingTransport doc comment) but reproduces the
// total, which is the part that answers "where did the wall clock go".
func TestReportTiming_SummarisesEveryRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fixedClock(t, 250*time.Millisecond)

	var log bytes.Buffer
	tr := newTimingTransport(http.DefaultTransport, &log)
	t.Cleanup(func() { activeTiming.Store(nil) })
	client := &http.Client{Transport: tr}

	for range 3 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/zones", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()
	}

	// newTimingTransport registered itself, so main's package-level call works.
	ReportTiming()
	if got, want := log.String(), "timing: total 3 request(s) in 750ms\n"; !strings.HasSuffix(got, want) {
		t.Errorf("timing output should end with %q, got:\n%s", want, got)
	}
}

// Without --timing there is no transport, so the summary must stay silent
// rather than print an empty total.
func TestReportTiming_SilentWithoutTiming(_ *testing.T) {
	activeTiming.Store(nil)
	ReportTiming() // must not panic
}

// A --timing run that made no requests (a validation error before auth) also
// prints nothing.
func TestReportTiming_SilentWhenNoRequestsWereMade(t *testing.T) {
	var log bytes.Buffer
	newTimingTransport(http.DefaultTransport, &log)
	t.Cleanup(func() { activeTiming.Store(nil) })
	ReportTiming()
	if log.Len() != 0 {
		t.Errorf("expected no output, got %q", log.String())
	}
}
