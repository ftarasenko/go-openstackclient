package kube

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// HTTP transport policy for the Kubernetes client. It mirrors the one in
// internal/auth (see internal/auth/transport.go) and internal/vault; the three
// copies exist because internal/auth imports this package, so a shared helper
// there would be an import cycle. Keep them in sync.
const (
	// defaultTimeout caps a single apiserver exchange when Options.Timeout is unset.
	defaultTimeout = 30 * time.Second

	// responseHeaderTimeout bounds "connected but silent" independently of the
	// whole-exchange timeout.
	responseHeaderTimeout = 60 * time.Second

	maxRedirects = 5
)

// credentialHeaders are dropped when a redirect leaves the origin host. The
// bearer token is in Authorization, which net/http does strip cross-host, but the
// stripping is worth being explicit about and the list also covers a proxy's own
// credentials.
var credentialHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
}

// newHTTPClient builds the apiserver client: a clone of http.DefaultTransport —
// which is what keeps HTTPS_PROXY/NO_PROXY, the dial timeout,
// TLSHandshakeTimeout, IdleConnTimeout and HTTP/2 working, all of which a bare
// &http.Transport{} throws away — plus the kubeconfig's TLS config, a request
// timeout and the same-host redirect policy.
func newHTTPClient(tlsCfg *tls.Config, timeout time.Duration) *http.Client {
	// net/http documents DefaultTransport as *http.Transport.
	tr := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // documented type of http.DefaultTransport
	tr.TLSClientConfig = tlsCfg
	tr.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: sameHostRedirect,
	}
}

// sameHostRedirect follows a redirect only with the credentials when it stays on
// the origin host and scheme; a cross-host hop is still followed, but stripped,
// so an apiserver (or something impersonating one) cannot forward the bearer
// token elsewhere. The origin is via[0] because net/http rebuilds each redirected
// request from the original headers.
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

// warnInsecure and warnCleartext are the kube-package copies of the warnings
// internal/auth emits, so a kubeconfig that opts out of verification or names an
// http:// apiserver is not silent about it — the requests koc makes here read
// credentials out of Secrets.
func warnInsecure(target string) {
	fmt.Fprintf(os.Stderr,
		"WARNING: TLS certificate verification is disabled for %s; connections are not secure\n", target)
}

func warnCleartext(target, rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" {
		return
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return
	}
	fmt.Fprintf(os.Stderr,
		"WARNING: %s is plain HTTP (%s); the bearer token and the Secrets it reads are sent unencrypted\n",
		target, rawURL)
}
