package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"regexp"
	"sort"
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

// tokenHeaderRe matches the value of credential-bearing headers for redaction.
// Authorization covers both the basic-auth standalone ironic path and a bearer
// token; Proxy-Authorization covers a proxy's own credentials.
// X-Service-Token is keystoneauth's service token; Cookie and Set-Cookie carry
// a session wherever a load balancer or a web front end adds one.
var tokenHeaderRe = regexp.MustCompile(`(?i)^(X-Auth-Token|X-Subject-Token|X-Service-Token|X-Vault-Token|Authorization|Proxy-Authorization|Cookie|Set-Cookie):\s*.*$`)

// secretKeys are the JSON keys whose scalar value is a credential and must never
// be printed. Matching is by KEY, case-insensitively, at any depth — matching the
// value's shape instead is how adminPass (server create/rescue/evacuate/password
// set) and private_key (keypair create, which nova returns exactly once) used to
// print verbatim.
//
// Only scalars are redacted: `"token": { … }` in a Keystone response is the token
// *object*, whose catalog is the single most useful thing --debug shows, and the
// token string itself travels in the X-Subject-Token header, which is redacted
// above.
var secretKeys = map[string]bool{
	"password":                      true,
	"secret":                        true,
	"application_credential_secret": true,
	"passcode":                      true,
	"adminpass":                     true,
	"admin_pass":                    true,
	"private_key":                   true,
	"secret_id":                     true,
	"role_id":                       true,
	"token":                         true,
	"blob":                          true,
	// cinder's volume transfer: whoever holds it can take the volume.
	"auth_key": true,
	// ironic's SNMP driver_info (snmp_priv_key); snmp_auth_key matches auth_key.
	"priv_key": true,
}

// isSecretKey reports whether a JSON key names a credential: it is on
// secretKeys, or one of its underscore-separated tails is — so ironic's
// ipmi_password and redfish_password, keystone's original_password and nova's
// rescue_password are caught without listing every driver's spelling.
// Unlike IsSecretName it does not match a single inner segment, so a field
// that merely mentions a secret (password_expires_at) stays readable.
func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	for {
		if secretKeys[k] {
			return true
		}
		i := strings.IndexByte(k, '_')
		if i < 0 {
			return false
		}
		k = k[i+1:]
	}
}

// IsSecretName reports whether a flag or key name carries a credential, by the
// same list --debug redacts JSON keys with: the name is lower-cased, dashes
// become underscores, and it matches when any underscore-separated tail of it
// (os_password → password, vault_secret_id → secret_id) or any one segment of
// it (secret_key → secret) is on the list. `koc shell` uses it for its audit
// events and history file, so a value that never reaches a --debug dump never
// reaches those either.
func IsSecretName(name string) bool {
	segs := strings.Split(strings.ToLower(strings.ReplaceAll(strings.TrimLeft(name, "-"), "-", "_")), "_")
	for i := range segs {
		if secretKeys[strings.Join(segs[i:], "_")] || secretKeys[segs[i]] {
			return true
		}
	}
	return false
}

// secretJSONRe is the fallback for a body that is not valid JSON (a truncated
// dump, a form-encoded payload). The value pattern tolerates escaped quotes:
// `"[^"]*"` stopped at the backslash-quote inside a password, leaking its tail
// and corrupting the rest of the dump.
var secretJSONRe = regexp.MustCompile(`(?i)"((?:[a-z0-9]+_)*(?:` + secretKeyAlternation + `))"\s*:\s*"(?:[^"\\]|\\.)*"`)

const redactedValue = "<redacted>"

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

// secretKeyAlternation is the regexp alternation of secretKeys, so the fallback
// path and the JSON path share one denylist.
var secretKeyAlternation = func() string {
	keys := make([]string, 0, len(secretKeys))
	for k := range secretKeys {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	sort.Strings(keys) // deterministic, and irrelevant to matching: the key is anchored by quotes
	return strings.Join(keys, "|")
}()

// redact removes credentials from one dumped HTTP message: the credential-bearing
// headers line by line, then the body by JSON key.
func redact(s string) string {
	head, sep, body := splitMessage(s)
	head = redactHeaders(head)
	if sep == "" {
		return head
	}
	return head + sep + redactBody(body)
}

// splitMessage separates the start line + headers from the body, returning the
// exact separator so the dump is reassembled byte for byte.
func splitMessage(s string) (head, sep, body string) {
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[:i], "\r\n\r\n", s[i+4:]
	}
	if i := strings.Index(s, "\n\n"); i >= 0 {
		return s[:i], "\n\n", s[i+2:]
	}
	return s, "", ""
}

func redactHeaders(head string) string {
	lines := strings.Split(head, "\n")
	for i, line := range lines {
		if !tokenHeaderRe.MatchString(line) {
			continue
		}
		if c := strings.IndexByte(line, ':'); c >= 0 {
			// Keep any trailing \r so CRLF framing survives.
			cr := ""
			if strings.HasSuffix(line, "\r") {
				cr = "\r"
			}
			lines[i] = line[:c] + ": " + redactedValue + cr
		}
	}
	return strings.Join(lines, "\n")
}

// redactBody redacts by JSON key when the body parses as JSON — the only way to
// catch every credential field wherever it is nested — and falls back to the
// regexp when it does not (a chunked or truncated dump, a form-encoded body).
// The JSON path re-encodes, so a redacted body is reformatted; that is a fair
// price for not leaking a password.
func redactBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed != "" && (trimmed[0] == '{' || trimmed[0] == '[') {
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			if out, err := marshalUnescaped(redactJSON(v)); err == nil {
				return out
			}
		}
	}
	return redactSecretValues(body)
}

// marshalUnescaped re-encodes a redacted body without json.Marshal's HTML
// escaping, so a dump stays readable (and "<redacted>" does not come out as
// "<redacted>").
func marshalUnescaped(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// redactJSON walks a decoded JSON document and replaces the scalar value of every
// denylisted key, at any depth, inside objects and arrays alike.
func redactJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSecretKey(k) && isScalar(val) {
				t[k] = redactedValue
				continue
			}
			// Token authentication (--os-token) sends the token as
			// identity.token.id. A v3 token *response* object carries no "id",
			// so redacting it here never hides the catalog.
			if tok, ok := val.(map[string]any); ok && strings.EqualFold(k, "token") && isScalar(tok["id"]) && tok["id"] != nil {
				tok["id"] = redactedValue
			}
			t[k] = redactJSON(val)
		}
		redactPatchValue(t)
		return t
	case []any:
		for i, val := range t {
			t[i] = redactJSON(val)
		}
		return t
	default:
		return v
	}
}

// redactPatchValue redacts one JSON-patch operation that writes a credential:
// ironic's `node set --driver-info ipmi_password=…` sends
// {"op":"add","path":"/driver_info/ipmi_password","value":"…"}, where the secret
// sits under "value" and only the path names it.
func redactPatchValue(op map[string]any) {
	path, ok := op["path"].(string)
	if !ok {
		return
	}
	if _, ok := op["op"]; !ok {
		return
	}
	if val, ok := op["value"]; ok && isScalar(val) && val != nil && isSecretKey(path[strings.LastIndexByte(path, '/')+1:]) {
		op["value"] = redactedValue
	}
}

// isScalar reports whether a decoded JSON value is a leaf, i.e. the kind of value
// that can itself be a credential.
func isScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

func redactSecretValues(s string) string {
	return secretJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		if c := strings.IndexByte(m, ':'); c >= 0 {
			return m[:c] + `: "` + redactedValue + `"`
		}
		return m
	})
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
// can print the summary once the command has finished. An invocation
// authenticates at most once, so a single handle is enough; it stays nil when
// --timing was not given, which is what makes ReportTiming a no-op then.
// ReportTiming takes it back, so a `koc shell` line without --timing does not
// repeat the previous line's summary.
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
	if t := activeTiming.Swap(nil); t != nil {
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
