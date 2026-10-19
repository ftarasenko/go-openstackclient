package auth

import (
	"context"
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
