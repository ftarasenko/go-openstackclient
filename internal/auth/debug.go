package auth

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"regexp"
	"strings"
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
