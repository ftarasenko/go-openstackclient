package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// captureStderr runs f with os.Stderr replaced by a pipe and returns what was
// written. The debug and warning paths write there by design (structured output
// owns stdout), so that is where the assertions have to look.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	f()

	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// Every credential-bearing header must be redacted, not just the two Keystone
// ones: --creds-from-ns sends Authorization (basic auth), Vault sends
// X-Vault-Token, and a proxy may need Proxy-Authorization.
func TestRedact_CredentialHeaders(t *testing.T) {
	dump := strings.Join([]string{
		"GET /v1/nodes HTTP/1.1",
		"Host: ironic.example.com",
		"Authorization: Basic aXJvbmljOnB3",
		"X-Auth-Token: gAAAAAsecret",
		"X-Subject-Token: gAAAAAother",
		"X-Vault-Token: hvs.abc123",
		"Proxy-Authorization: Basic cHJveHk6cHc=",
		"User-Agent: koc",
		"", "",
	}, "\r\n")

	got := redact(dump)
	for _, leak := range []string{"aXJvbmljOnB3", "gAAAAAsecret", "gAAAAAother", "hvs.abc123", "cHJveHk6cHc="} {
		if strings.Contains(got, leak) {
			t.Errorf("redacted dump still contains %q:\n%s", leak, got)
		}
	}
	// A non-credential header is untouched.
	if !strings.Contains(got, "User-Agent: koc") {
		t.Errorf("redaction ate an ordinary header:\n%s", got)
	}
	if n := strings.Count(got, redactedValue); n != 5 {
		t.Errorf("got %d redactions, want 5:\n%s", n, got)
	}
}

// The regression this fix is for: redaction keyed on a handful of field names let
// nova's adminPass and the one-time private_key of a created keypair print in
// full.
func TestRedact_BodySecretsByKey(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		leaks []string
	}{
		{
			name:  "adminPass on server create",
			body:  `{"server":{"id":"s1","adminPass":"Hunter2Hunter2"}}`,
			leaks: []string{"Hunter2Hunter2"},
		},
		{
			name:  "admin_pass on password set",
			body:  `{"changePassword":{"admin_pass":"Hunter2Hunter2"}}`,
			leaks: []string{"Hunter2Hunter2"},
		},
		{
			name:  "keypair private key",
			body:  `{"keypair":{"name":"k","private_key":"-----BEGIN PRIVATE KEY-----\nMIIsecret\n"}}`,
			leaks: []string{"BEGIN PRIVATE KEY", "MIIsecret"},
		},
		{
			name:  "nested auth body",
			body:  `{"auth":{"identity":{"password":{"user":{"name":"admin","password":"p@ss"}}}}}`,
			leaks: []string{"p@ss"},
		},
		{
			name:  "inside an array",
			body:  `{"credentials":[{"blob":"{\"access\":\"a\"}"},{"secret":"sh"}]}`,
			leaks: []string{"access", "sh\""},
		},
		{
			name:  "vault approle login",
			body:  `{"role_id":"rid-1","secret_id":"sid-1"}`,
			leaks: []string{"rid-1", "sid-1"},
		},
		{
			name:  "application credential",
			body:  `{"application_credential":{"application_credential_secret":"acs","passcode":"123456"}}`,
			leaks: []string{"acs", "123456"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact("POST /x HTTP/1.1\r\nHost: h\r\n\r\n" + tc.body)
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Errorf("body leaked %q:\n%s", leak, got)
				}
			}
			if !strings.Contains(got, redactedValue) {
				t.Errorf("nothing was redacted:\n%s", got)
			}
		})
	}
}

// A password containing an escaped quote used to defeat the `"[^"]*"` value
// pattern: the match stopped at the backslash, printing the rest of the password
// and corrupting the dump. Both the JSON path and the regexp fallback must handle
// it.
func TestRedact_PasswordWithEscapedQuote(t *testing.T) {
	const tail = "tail-of-the-password"
	body := `{"auth":{"identity":{"password":{"user":{"password":"pre\"` + tail + `"}}}}}`

	if got := redact("POST /v3/auth/tokens HTTP/1.1\r\nHost: h\r\n\r\n" + body); strings.Contains(got, tail) {
		t.Errorf("JSON path leaked the password tail:\n%s", got)
	}

	// Same body, but not parseable as JSON (a truncated dump), so the fallback runs.
	truncated := body + ` trailing garbage {`
	if got := redact("POST /v3/auth/tokens HTTP/1.1\r\nHost: h\r\n\r\n" + truncated); strings.Contains(got, tail) {
		t.Errorf("regexp fallback leaked the password tail:\n%s", got)
	}
}

// The Keystone response's "token" is an object whose catalog is the most useful
// thing --debug shows; only a scalar named token is a credential.
func TestRedact_KeepsTokenObjectButRedactsTokenString(t *testing.T) {
	body := `{"token":{"expires_at":"2030-01-01T00:00:00Z","catalog":[{"type":"compute","id":"c1"}]}}`
	got := redact("HTTP/1.1 201 Created\r\nContent-Type: application/json\r\n\r\n" + body)
	for _, want := range []string{"catalog", "compute", "expires_at"} {
		if !strings.Contains(got, want) {
			t.Errorf("token object lost %q, which --debug exists to show:\n%s", want, got)
		}
	}

	scalar := `{"token":"hvs.scalar-secret"}`
	if got := redact("HTTP/1.1 200 OK\r\n\r\n" + scalar); strings.Contains(got, "hvs.scalar-secret") {
		t.Errorf("a scalar token was not redacted:\n%s", got)
	}
}

// The transport itself: both directions are dumped, and the secrets in them are
// gone by the time they reach stderr.
func TestDebugTransport_DumpsBothDirectionsRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Subject-Token", "gAAAAAresponsetoken")
		_, _ = w.Write([]byte(`{"server":{"id":"s1","adminPass":"Hunter2Hunter2"}}`))
	}))
	defer srv.Close()

	out := captureStderr(t, func() {
		client := &http.Client{Transport: newDebugTransport(http.DefaultTransport)}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v2.1/servers",
			strings.NewReader(`{"server":{"name":"n","adminPass":"Hunter2Hunter2"}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Auth-Token", "gAAAAArequesttoken")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})

	if !strings.Contains(out, "> POST /v2.1/servers") || !strings.Contains(out, "< HTTP/1.1 200 OK") {
		t.Errorf("both directions should be dumped:\n%s", out)
	}
	for _, leak := range []string{"gAAAAArequesttoken", "gAAAAAresponsetoken", "Hunter2Hunter2"} {
		if strings.Contains(out, leak) {
			t.Errorf("--debug leaked %q:\n%s", leak, out)
		}
	}
}
