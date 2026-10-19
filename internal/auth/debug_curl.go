package auth

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

// roundTripCurl is --debug-format curl: keystoneauth's session log, the layout
// `openstack --debug` prints. A request is one `REQ: curl …` line that replays
// it from a shell (with the credentials replaced by <redacted>, so a replay
// needs the token put back by hand); a response is `RESP: [status] headers`
// followed by `RESP BODY: …`.
func (d *debugTransport) roundTripCurl(req *http.Request) (*http.Response, error) {
	fmt.Fprintln(os.Stderr, curlRequest(req, d.tlsArgs))

	resp, err := d.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	fmt.Fprintln(os.Stderr, curlResponse(resp))
	return resp, nil
}

// curlRequest renders req as keystoneauth does — `curl -g -i`, the TLS options,
// `-X METHOD URL`, one -H per header in name order, then -d with the body —
// but single-quoted, so the line survives a paste into any POSIX shell.
func curlRequest(req *http.Request, tlsArgs []string) string {
	var b strings.Builder
	b.WriteString("REQ: curl -g -i")
	for _, a := range tlsArgs {
		b.WriteString(" " + a)
	}
	fmt.Fprintf(&b, " -X %s %s", req.Method, shellQuote(req.URL.Redacted()))
	for _, line := range headerLines(req.Header) {
		b.WriteString(" -H " + shellQuote(line))
	}
	if body, ok := requestBody(req); ok {
		b.WriteString(" -d " + shellQuote(body))
	}
	return b.String()
}

// curlResponse renders resp as keystoneauth does: the status and every header
// on one line, then the body, or why it was left out.
func curlResponse(resp *http.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "RESP: [%d]", resp.StatusCode)
	for _, line := range headerLines(resp.Header) {
		b.WriteString(" " + line)
	}
	b.WriteString("\nRESP BODY: ")
	b.WriteString(responseBody(resp))
	return b.String()
}

// headerLines returns "Name: value" per header value, sorted by name and
// redacted by the same rule as the HTTP dump.
func headerLines(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	var lines []string
	for _, k := range names {
		for _, v := range h[k] {
			lines = append(lines, redactHeaders(k+": "+v))
		}
	}
	return lines
}

// requestBody returns the redacted request body, reading it without consuming
// what the transport will send. A binary or large body is named, not printed.
func requestBody(req *http.Request) (string, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return "", false
	}
	if !dumpBody(req.Header) {
		return omittedBody(req.Header, req.ContentLength), true
	}
	var raw []byte
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			return "<body unreadable>", true
		}
		raw, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "<body unreadable>", true
		}
	} else {
		var err error
		raw, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(raw))
		if err != nil {
			return "<body unreadable>", true
		}
	}
	return redactBody(string(raw)), true
}

// responseBody returns the redacted response body and puts an unread copy back
// for the caller. Like keystoneauth, a non-JSON body is not logged.
func responseBody(resp *http.Response) string {
	if !dumpBody(resp.Header) {
		return omittedBody(resp.Header, resp.ContentLength)
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		return ""
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return "<body unreadable>"
	}
	return redactBody(string(raw))
}

func omittedBody(h http.Header, n int64) string {
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "unset"
	}
	if n >= 0 {
		return fmt.Sprintf("<omitted: %d bytes, Content-Type %s>", n, ct)
	}
	return fmt.Sprintf("<omitted: Content-Type %s>", ct)
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// curlTLSArgs are the curl options that reproduce koc's TLS settings for an
// endpoint: the verification switch it actually ended up with, and the file
// paths given on the line or in OS_*. A clouds.yaml cacert/cert is not
// reproduced: gophercloud reads it into the config without keeping the path.
func (o *Options) curlTLSArgs(insecure bool) []string {
	var args []string
	if insecure {
		args = append(args, "--insecure")
	}
	if o.CACert != "" {
		args = append(args, "--cacert", shellQuote(o.CACert))
	}
	if o.ClientCert != "" && o.ClientKey != "" {
		args = append(args, "--cert", shellQuote(o.ClientCert), "--key", shellQuote(o.ClientKey))
	}
	return args
}
