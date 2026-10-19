package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// --debug-format curl prints keystoneauth's layout: a pasteable curl line per
// request and RESP/RESP BODY per response, with the same redaction as the
// HTTP dump, and without consuming either body.
func TestDebugTransport_Curl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"server":{"name":"it's","adminPass":"Hunter2Hunter2"}}` {
			t.Errorf("server got body %q: the dump consumed it", b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Subject-Token", "gAAAAAresponsetoken")
		w.Header().Set("Set-Cookie", "session=cookiesecret")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"s1","adminPass":"Hunter2Hunter2"}}`))
	}))
	defer srv.Close()

	var body string
	out := captureStderr(t, func() {
		client := &http.Client{Transport: newDebugTransport(http.DefaultTransport, DebugFormatCurl, []string{"--insecure"})}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v2.1/servers?a=1&b=2",
			strings.NewReader(`{"server":{"name":"it's","adminPass":"Hunter2Hunter2"}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Auth-Token", "gAAAAArequesttoken")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		_ = resp.Body.Close()
	})

	want := `REQ: curl -g -i --insecure -X POST '` + srv.URL + `/v2.1/servers?a=1&b=2'` +
		` -H 'Content-Type: application/json' -H 'X-Auth-Token: <redacted>'` +
		` -d '{"server":{"adminPass":"<redacted>","name":"it'\''s"}}'`
	if !strings.Contains(out, want+"\n") {
		t.Errorf("curl line:\n got %s\nwant %s", out, want)
	}
	if !strings.Contains(out, "RESP: [202] ") || !strings.Contains(out, "Set-Cookie: <redacted>") ||
		!strings.Contains(out, "X-Subject-Token: <redacted>") {
		t.Errorf("response line wrong:\n%s", out)
	}
	if !strings.Contains(out, `RESP BODY: {"server":{"adminPass":"<redacted>","id":"s1"}}`) {
		t.Errorf("response body wrong:\n%s", out)
	}
	for _, leak := range []string{"gAAAAArequesttoken", "gAAAAAresponsetoken", "Hunter2Hunter2", "cookiesecret"} {
		if strings.Contains(out, leak) {
			t.Errorf("--debug-format curl leaked %q:\n%s", leak, out)
		}
	}
	if body != `{"server":{"id":"s1","adminPass":"Hunter2Hunter2"}}` {
		t.Errorf("caller got body %q: the dump consumed it", body)
	}
}

// A binary body is named, not printed, in both directions.
func TestDebugTransport_CurlOmitsBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("\x00\x01RAWIMAGE"))
	}))
	defer srv.Close()

	out := captureStderr(t, func() {
		client := &http.Client{Transport: newDebugTransport(nil, DebugFormatCurl, nil)}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL+"/v2/images/i/file",
			strings.NewReader("\x00\x01UPLOAD"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	if strings.Contains(out, "UPLOAD") || strings.Contains(out, "RAWIMAGE") {
		t.Errorf("binary body dumped:\n%s", out)
	}
	if !strings.Contains(out, `-d '<omitted: 8 bytes, Content-Type application/octet-stream>'`) ||
		!strings.Contains(out, "RESP BODY: <omitted: 10 bytes, Content-Type application/octet-stream>") {
		t.Errorf("omission not named:\n%s", out)
	}
}

func TestCurlTLSArgs(t *testing.T) {
	o := &Options{CACert: "/etc/ca dir/ca.pem", ClientCert: "/c.pem", ClientKey: "/k.pem"}
	got := strings.Join(o.curlTLSArgs(true), " ")
	if want := `--insecure --cacert '/etc/ca dir/ca.pem' --cert '/c.pem' --key '/k.pem'`; got != want {
		t.Errorf("curlTLSArgs = %s, want %s", got, want)
	}
}

func TestDebugFormatFlag(t *testing.T) {
	t.Setenv("KOC_DEBUG_FORMAT", "")
	o := &Options{}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	o.AddFlags(fs)
	if o.DebugFormat != DebugFormatHTTP {
		t.Errorf("default = %q, want http", o.DebugFormat)
	}
	if err := fs.Parse([]string{"--debug-format", "curl"}); err != nil || o.DebugFormat != DebugFormatCurl {
		t.Errorf("--debug-format curl: %v, %q", err, o.DebugFormat)
	}
	if err := fs.Parse([]string{"--debug-format", "har"}); err == nil {
		t.Error("--debug-format har was accepted")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("reset") }

// A body that cannot be read is named rather than failing the request, and a
// body with no length says only its type.
func TestCurlBodies_Unreadable(t *testing.T) {
	newReq := func(body io.Reader) *http.Request {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/", body)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	getBodyFails := newReq(strings.NewReader("{}"))
	getBodyFails.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	getBodyReadFails := newReq(strings.NewReader("{}"))
	getBodyReadFails.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(failingReader{}), nil }
	bodyReadFails := newReq(io.NopCloser(failingReader{}))
	streamed := newReq(io.NopCloser(strings.NewReader(`{"a":1}`)))

	for name, tc := range map[string]struct {
		req  *http.Request
		want string
	}{
		"GetBody fails":      {getBodyFails, bodyUnreadable},
		"GetBody read fails": {getBodyReadFails, bodyUnreadable},
		"body read fails":    {bodyReadFails, bodyUnreadable},
		"no GetBody":         {streamed, `{"a":1}`},
	} {
		if got, ok := requestBody(tc.req); !ok || got != tc.want {
			t.Errorf("%s: requestBody = %q, %v; want %q", name, got, ok, tc.want)
		}
	}
	// The streamed body was put back for the transport to send.
	if b, _ := io.ReadAll(streamed.Body); string(b) != `{"a":1}` {
		t.Errorf("streamed body after logging = %q", b)
	}
	if _, ok := requestBody(newReq(nil)); ok {
		t.Error("a request without a body logged one")
	}

	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(failingReader{})}
	if got := responseBody(resp); got != bodyUnreadable {
		t.Errorf("responseBody = %q", got)
	}
	if got := responseBody(&http.Response{Header: http.Header{}, Body: http.NoBody}); got != "" {
		t.Errorf("empty responseBody = %q", got)
	}
	if got := omittedBody(http.Header{}, -1); got != "<omitted: Content-Type unset>" {
		t.Errorf("omittedBody = %q", got)
	}
}
