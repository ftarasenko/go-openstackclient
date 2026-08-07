package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// debugTransport logs each HTTP request and response to stderr when --debug is
// set. It redacts auth tokens and credential values so secrets never leak into
// logs, and skips dumping large or binary payloads (e.g. image up/downloads) so
// debug logging does not buffer multi-GB bodies in memory or spew binary to the
// terminal.
type debugTransport struct {
	rt http.RoundTripper
}

func newDebugTransport(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &debugTransport{rt: rt}
}

// maxDumpBody caps how much of a textual body we are willing to dump.
const maxDumpBody = 1 << 20 // 1 MiB

// tokenHeaderRe matches the value of token-bearing headers for redaction.
var tokenHeaderRe = regexp.MustCompile(`(?i)^(X-Auth-Token|X-Subject-Token):\s*.*$`)

// secretJSONRe matches JSON string values of credential fields so the re-auth
// request body — which gophercloud re-POSTs with AllowReauth — never prints
// plaintext credentials. Scoped to genuine secrets to avoid redacting resource
// IDs or the token object in responses.
var secretJSONRe = regexp.MustCompile(`(?i)"(password|secret|application_credential_secret|passcode)"\s*:\s*"[^"]*"`)

func (d *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if dump, err := httputil.DumpRequestOut(req, dumpBody(req.Header)); err == nil {
		fmt.Fprintf(os.Stderr, "> %s\n", redact(string(dump)))
	}

	resp, err := d.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if dump, derr := httputil.DumpResponse(resp, dumpBody(resp.Header)); derr == nil {
		fmt.Fprintf(os.Stderr, "< %s\n", redact(string(dump)))
	}
	return resp, nil
}

// dumpBody reports whether a message body should be included in the dump. Only
// small, textual (JSON) bodies are dumped; binary or large payloads are elided
// to protect memory and the terminal.
func dumpBody(h http.Header) bool {
	ct := h.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "json") && !strings.HasPrefix(ct, "text/") {
		return false
	}
	if cl := h.Get("Content-Length"); cl != "" {
		var n int64
		if _, err := fmt.Sscan(cl, &n); err == nil && n > maxDumpBody {
			return false
		}
	}
	return true
}

func redact(s string) string {
	// Redact token-bearing headers (line-oriented).
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if tokenHeaderRe.MatchString(line) {
			if c := strings.IndexByte(line, ':'); c >= 0 {
				lines[i] = line[:c] + ": <redacted>"
			}
		}
	}
	out := strings.Join(lines, "\n")
	// Redact credential values that appear in a JSON auth body.
	out = secretJSONRe.ReplaceAllStringFunc(out, func(m string) string {
		if c := strings.IndexByte(m, ':'); c >= 0 {
			return m[:c] + `: "<redacted>"`
		}
		return m
	})
	return out
}

// timingTransport prints the wall-clock duration of every HTTP round trip to
// stderr, backing --timing. It is separate from debugTransport so timings can be
// collected without the full request/response dumps: `openstack --timing` prints
// only a per-call table, and that is the useful signal when chasing a slow
// command rather than a wrong one.
//
// It wraps whatever transport it is given, so with both flags set the timing line
// follows the debug dump for the same call.
//
// # Deliberate deviation from upstream
//
// osc_lib/command/timing.py is a cliff Lister: it prints a "URL | Seconds"
// table to STDOUT, with a final Total row. koc writes plain lines to STDERR
// instead, because timing output on stdout would corrupt `koc … -f json | jq`
// and `koc … -f value > file` — the two things koc's output layer exists to
// make reliable. Upstream can afford it because its table is itself a cliff
// formatter; koc's -f applies to the command's result, not to its diagnostics.
// Recorded in docs/coverage.md under "Naming deviations".
//
// Upstream's Total row is genuinely useful, though, and is reproduced by
// ReportTiming.
type timingTransport struct {
	rt http.RoundTripper
	w  io.Writer

	mu    sync.Mutex
	calls int
	total time.Duration
}

// activeTiming is the transport --timing installed for this invocation, so main
// can print the summary once the command has finished. koc authenticates at most
// once per process, so a single handle is enough; it stays nil when --timing was
// not given, which is what makes ReportTiming a no-op then.
var activeTiming atomic.Pointer[timingTransport]

func newTimingTransport(rt http.RoundTripper, w io.Writer) *timingTransport {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if w == nil {
		w = os.Stderr
	}
	t := &timingTransport{rt: rt, w: w}
	activeTiming.Store(t)
	return t
}

// ReportTiming writes the --timing summary — upstream's Total row — and does
// nothing when --timing was not given. Call it once the command has run,
// including when it failed: a slow call is often exactly why it failed.
func ReportTiming() {
	if t := activeTiming.Load(); t != nil {
		t.report()
	}
}

func (t *timingTransport) report() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == 0 {
		return
	}
	_, _ = fmt.Fprintf(t.w, "timing: total %d request(s) in %s\n",
		t.calls, t.total.Round(time.Millisecond))
}

func (t *timingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := timeNow()
	resp, err := t.rt.RoundTrip(req)
	elapsed := timeNow().Sub(start)

	status := "error"
	if resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	// gophercloud reuses one client across goroutines for parallel calls, so the
	// writes are serialised to keep lines from interleaving.
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.total += elapsed
	_, _ = fmt.Fprintf(t.w, "timing: %-6s %s %s in %s\n", req.Method, req.URL.Redacted(), status, elapsed.Round(time.Millisecond))
	return resp, err
}

// timeNow is a variable so tests can make durations deterministic.
var timeNow = time.Now
