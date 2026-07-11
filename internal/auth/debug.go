package auth

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"regexp"
)

// debugTransport logs each HTTP request and response to stderr when --debug is
// set, redacting auth tokens so credentials never leak into logs.
type debugTransport struct {
	rt http.RoundTripper
}

func newDebugTransport(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &debugTransport{rt: rt}
}

// tokenHeaderRe matches the value of token-bearing headers for redaction.
var tokenHeaderRe = regexp.MustCompile(`(?i)(X-Auth-Token|X-Subject-Token):\s*.*`)

func (d *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if dump, err := httputil.DumpRequestOut(req, true); err == nil {
		fmt.Fprintf(os.Stderr, "> %s\n", redact(string(dump)))
	}

	resp, err := d.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if dump, derr := httputil.DumpResponse(resp, true); derr == nil {
		fmt.Fprintf(os.Stderr, "< %s\n", redact(string(dump)))
	}
	return resp, nil
}

func redact(s string) string {
	return tokenHeaderRe.ReplaceAllStringFunc(s, func(line string) string {
		if i := indexColon(line); i >= 0 {
			return line[:i+1] + " <redacted>"
		}
		return line
	})
}

func indexColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
