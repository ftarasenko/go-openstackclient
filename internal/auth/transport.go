package auth

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// HTTP transport policy shared by every client koc builds.
//
// The same three rules are implemented in internal/vault and internal/kube. They
// are deliberately duplicated rather than factored into a shared package:
// internal/auth already imports both of those, so a common helper there would be
// an import cycle, and a fourth package for twenty lines is worse than three
// small copies that name each other. Keep them in sync.
const (
	// responseHeaderTimeout bounds "connected, but the endpoint never started
	// answering" independently of the whole-exchange Timeout, so a wedged API is
	// reported in a minute instead of five.
	responseHeaderTimeout = 60 * time.Second

	// maxRedirects caps a redirect chain. Vault legitimately 307s standby→active
	// and glance/swift legitimately 302 to a data endpoint, so redirects are
	// followed — but not indefinitely.
	maxRedirects = 5
)

// credentialHeaders are the request headers that carry an identity. Go's own
// redirect handling strips only Authorization, Cookie and Www-Authenticate when
// the host changes; every token header OpenStack, Vault and Kubernetes use is
// invisible to it, so they are dropped here.
var credentialHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"X-Auth-Token",
	"X-Subject-Token",
	"X-Vault-Token",
}

// newHTTPTransport returns the transport every OpenStack-facing client uses: a
// clone of http.DefaultTransport — which is what keeps HTTPS_PROXY/NO_PROXY, the
// 30s dial timeout, TLSHandshakeTimeout, IdleConnTimeout and HTTP/2 working — with
// koc's TLS config folded in.
//
// The TLS config must be folded in *here* rather than passed to gophercloud's
// config.WithTLSConfig: that option replaces httpClient.Transport wholesale and
// is applied last, so it would silently discard this transport (and with it the
// proxy support, the debug dump and the timing instrumentation).
func newHTTPTransport(tlsCfg *tls.Config) *http.Transport {
	// net/http documents DefaultTransport as *http.Transport.
	tr := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // documented type of http.DefaultTransport
	tr.TLSClientConfig = tlsCfg
	tr.ResponseHeaderTimeout = responseHeaderTimeout
	return tr
}

// sameHostRedirect is the CheckRedirect policy for a credential-bearing client.
// A redirect that stays on the origin host and scheme is followed with the
// credentials intact; one that leaves it is still followed (a redirect to a
// public data endpoint is legitimate) but with every credential header removed,
// so a hostile or misconfigured endpoint cannot harvest a token by answering
// 302.
//
// The comparison is against via[0]: net/http rebuilds each redirected request
// from the *original* headers, so a header deleted on one hop is back on the
// next unless the origin is what we test.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	origin := via[0].URL
	if req.URL.Host == origin.Host && req.URL.Scheme == origin.Scheme {
		return nil
	}
	for _, h := range credentialHeaders {
		req.Header.Del(h)
	}
	return nil
}

// instrument wraps a transport with the --debug dump and the --timing meter, in
// that order, so the timing line follows the dump for the same call. It is
// applied to the transport handed to gophercloud *before* authentication, so the
// token request, the returned catalog and an auth failure are all visible.
func (o *Options) instrument(rt http.RoundTripper) http.RoundTripper {
	if o.Debug {
		rt = newDebugTransport(rt)
	}
	if o.Timing {
		rt = newTimingTransport(rt, os.Stderr)
	}
	return rt
}

// httpClient assembles the client for an OpenStack endpoint: koc's transport,
// the --timeout cap and the same-host redirect policy.
func (o *Options) httpClient(tlsCfg *tls.Config) http.Client {
	return http.Client{
		Transport:     o.instrument(newHTTPTransport(tlsCfg)),
		Timeout:       o.Timeout,
		CheckRedirect: sameHostRedirect,
	}
}

// warnInsecure emits the mandated warning for a path that skips TLS
// verification. Every such path calls it — the Keystone endpoint, the standalone
// ironic endpoint, Vault and the Kubernetes apiserver — because "which
// connection is unverified" is the part the operator needs.
func warnInsecure(target string) {
	fmt.Fprintf(os.Stderr,
		"WARNING: TLS certificate verification is disabled for %s; connections are not secure\n", target)
}

// warnCleartext warns that credentials are about to be sent over plain HTTP.
// Nothing in koc requires TLS — an openrc, an Ironic CR without a certificate or
// a kubeconfig may all name an http:// endpoint — so the least it can do is say
// so out loud.
func warnCleartext(target, rawURL string) {
	if !isCleartextURL(rawURL) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"WARNING: %s is plain HTTP (%s); credentials and tokens are sent unencrypted\n", target, rawURL)
}

// isCleartextURL reports whether a URL uses http:// (and is not loopback, where
// there is no network to eavesdrop on).
func isCleartextURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return false
	}
	return true
}
