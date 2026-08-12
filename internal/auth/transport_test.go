package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// doGet issues a plain GET through client. It exists so the tests that expect a
// failure (redirect loop, timeout) still build the request with a context.
func doGet(t *testing.T, client *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client.Do(req)
}

// tokenEchoServer records the credential headers it received, so a redirect test
// can assert what a foreign host was handed.
func tokenEchoServer(t *testing.T, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A cross-host redirect must not carry the credentials. Go's own stripping covers
// Authorization and Cookie only, so before the fix X-Auth-Token reached a foreign
// host verbatim.
func TestSameHostRedirect_DropsCredentialsCrossHost(t *testing.T) {
	var foreignSaw http.Header
	foreign := tokenEchoServer(t, &foreignSaw)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, foreign.URL+"/landed", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer origin.Close()

	o := &Options{}
	client := o.httpClient(nil)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL+"/redirect", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Auth-Token", "gAAAAAsecret")
	req.Header.Set("Authorization", "Basic aXJvbmljOnB3")
	req.Header.Set("X-Vault-Token", "hvs.abc")
	req.Header.Set("Proxy-Authorization", "Basic cHJveHk=")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	for _, h := range []string{"X-Auth-Token", "Authorization", "X-Vault-Token", "Proxy-Authorization"} {
		if got := foreignSaw.Get(h); got != "" {
			t.Errorf("foreign host received %s: %q", h, got)
		}
	}
}

// A same-host redirect is legitimate (glance/swift 302 to a data endpoint, Vault
// 307 standby→active inside one host) and must keep working, credentials intact.
func TestSameHostRedirect_KeepsCredentialsSameHost(t *testing.T) {
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/landed", http.StatusFound)
			return
		}
		token = r.Header.Get("X-Auth-Token")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	o := &Options{}
	client := o.httpClient(nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/redirect", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Auth-Token", "gAAAAAsecret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if token != "gAAAAAsecret" {
		t.Errorf("same-host redirect lost the token (got %q)", token)
	}
}

func TestSameHostRedirect_HopCap(t *testing.T) {
	var hops int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer srv.Close()

	o := &Options{}
	client := o.httpClient(nil)
	_, err := doGet(t, &client, srv.URL+"/start") //nolint:bodyclose // the request never succeeds
	if err == nil {
		t.Fatal("an endless redirect loop should fail")
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Errorf("err = %v, want the redirect cap", err)
	}
	if hops > maxRedirects+1 {
		t.Errorf("followed %d hops, want at most %d", hops, maxRedirects+1)
	}
}

// A socket that accepts and never answers used to hang the command forever: the
// OpenStack path passed a zero-value http.Client.
func TestHTTPClient_TimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	o := &Options{Timeout: 150 * time.Millisecond}
	client := o.httpClient(nil)

	start := time.Now()
	_, err := doGet(t, &client, srv.URL) //nolint:bodyclose // the request never succeeds
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v to give up; the timeout is not being applied", elapsed)
	}
}

// The transport must be a clone of http.DefaultTransport, or a proxied air-gapped
// deployment loses HTTPS_PROXY/NO_PROXY along with every dial and handshake
// timeout.
func TestNewHTTPTransport_KeepsDefaultTransportBehavior(t *testing.T) {
	tr := newHTTPTransport(nil)
	if tr.Proxy == nil {
		t.Error("Proxy is nil: HTTPS_PROXY/NO_PROXY would be ignored")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout is unset")
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout is unset")
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("HTTP/2 is disabled")
	}
	if tr.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, responseHeaderTimeout)
	}
}

// keystoneMock answers the token request, recording what it saw. handler is the
// response for POST /v3/auth/tokens.
func keystoneMock(t *testing.T, status int, body string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/auth/tokens" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Subject-Token", "gAAAAAtoken")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func mockOptions(authURL string) *Options {
	return &Options{
		AuthURL:           authURL,
		Username:          "admin",
		Password:          "s3cret-password",
		ProjectName:       "admin",
		ProjectDomainName: "Default",
		UserDomainName:    "Default",
		Timeout:           10 * time.Second,
	}
}

const tokenResponse = `{"token":{"expires_at":"2035-01-01T00:00:00.000000Z","catalog":[],"project":{"id":"p1","name":"admin"}}}`

// --debug used to attach AFTER NewProviderClient had already POSTed
// /v3/auth/tokens, so the token request and the catalog it returned were
// invisible. The dump must now include them — with the password redacted.
func TestAuthenticate_DebugSeesTheTokenRequest(t *testing.T) {
	srv, calls := keystoneMock(t, http.StatusCreated, tokenResponse)
	o := mockOptions(srv.URL + "/v3")
	o.Debug = true

	var authErr error
	var client *Client
	out := captureStderr(t, func() { client, authErr = o.Authenticate(context.Background()) })
	if authErr != nil {
		t.Fatalf("Authenticate: %v", authErr)
	}
	if *calls != 1 {
		t.Fatalf("keystone saw %d token requests, want 1", *calls)
	}

	if !strings.Contains(out, "POST /v3/auth/tokens") {
		t.Errorf("--debug did not dump the token request:\n%s", out)
	}
	if !strings.Contains(out, "< HTTP/1.1 201 Created") {
		t.Errorf("--debug did not dump the token response:\n%s", out)
	}
	if strings.Contains(out, "s3cret-password") {
		t.Errorf("--debug leaked the password:\n%s", out)
	}
	if strings.Contains(out, "gAAAAAtoken") {
		t.Errorf("--debug leaked the issued token:\n%s", out)
	}

	// The client koc hands to gophercloud keeps the timeout and the redirect policy.
	if client.Provider.HTTPClient.Timeout != 10*time.Second {
		t.Errorf("provider timeout = %v, want 10s", client.Provider.HTTPClient.Timeout)
	}
	if client.Provider.HTTPClient.CheckRedirect == nil {
		t.Error("provider has no redirect policy")
	}
}

// On an authentication FAILURE --debug used to print nothing at all, because the
// transport was attached to a provider that was never returned.
func TestAuthenticate_DebugSeesAuthFailure(t *testing.T) {
	srv, _ := keystoneMock(t, http.StatusUnauthorized, `{"error":{"code":401,"message":"Invalid credentials"}}`)
	o := mockOptions(srv.URL + "/v3")
	o.Debug = true

	var err error
	out := captureStderr(t, func() { _, err = o.Authenticate(context.Background()) })
	if err == nil {
		t.Fatal("expected an authentication error")
	}
	if !strings.Contains(out, "POST /v3/auth/tokens") || !strings.Contains(out, "401") {
		t.Errorf("--debug must show the failed token request:\n%s", out)
	}
	if strings.Contains(out, "s3cret-password") {
		t.Errorf("--debug leaked the password:\n%s", out)
	}
}

// --timing must measure the token request too, for the same reason.
func TestAuthenticate_TimingSeesTheTokenRequest(t *testing.T) {
	srv, _ := keystoneMock(t, http.StatusCreated, tokenResponse)
	o := mockOptions(srv.URL + "/v3")
	o.Timing = true

	var err error
	out := captureStderr(t, func() { _, err = o.Authenticate(context.Background()) })
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !strings.Contains(out, "timing: POST") || !strings.Contains(out, "/v3/auth/tokens") {
		t.Errorf("--timing did not measure the token request:\n%s", out)
	}
}

// An http:// identity endpoint carries the password in the clear; koc says so.
func TestAuthenticate_WarnsOnCleartextEndpoint(t *testing.T) {
	srv, _ := keystoneMock(t, http.StatusCreated, tokenResponse)
	// httptest serves on 127.0.0.1, which isCleartextURL deliberately excuses, so
	// the warning is asserted on the helper with a routable host.
	if isCleartextURL(srv.URL) {
		t.Fatalf("loopback should not warn: %s", srv.URL)
	}
	out := captureStderr(t, func() { warnCleartext("the OpenStack identity endpoint", "http://keystone.example.com:5000/v3") })
	if !strings.Contains(out, "plain HTTP") {
		t.Errorf("no cleartext warning: %q", out)
	}
	quiet := captureStderr(t, func() { warnCleartext("the OpenStack identity endpoint", "https://keystone.example.com:5000/v3") })
	if quiet != "" {
		t.Errorf("https must not warn: %q", quiet)
	}
}
