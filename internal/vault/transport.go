package vault

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// HTTP transport policy for the Vault client. It mirrors the one in
// internal/auth (see internal/auth/transport.go) and internal/kube; the three
// copies exist because internal/auth imports this package, so a shared helper
// there would be an import cycle. Keep them in sync.
const (
	// defaultTimeout caps a single Vault exchange when Config.Timeout is unset.
	defaultTimeout = 30 * time.Second

	// responseHeaderTimeout bounds "connected but silent" independently of the
	// whole-exchange timeout.
	responseHeaderTimeout = 60 * time.Second

	// maxRedirects caps a redirect chain. Vault legitimately 307s a standby node
	// to the active one, so redirects are followed — see sameHostRedirect for what
	// travels with them.
	maxRedirects = 5
)

// credentialHeaders are dropped when a redirect leaves the origin host. Go's own
// stripping covers only Authorization/Cookie/Www-Authenticate, which is exactly
// not the header a Vault token travels in.
var credentialHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"X-Vault-Token",
	"X-Vault-Namespace",
}

// newHTTPClient builds the Vault client's http.Client: a clone of
// http.DefaultTransport (so HTTPS_PROXY/NO_PROXY, the dial timeout,
// TLSHandshakeTimeout, IdleConnTimeout and HTTP/2 all keep working — a
// hand-rolled &http.Transport{} loses every one of them) with the caller's TLS
// config, a request timeout, and the same-host redirect policy.
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

// sameHostRedirect follows a redirect that stays on the origin host and scheme
// with the token intact, and one that leaves it without any credential header —
// a Vault standby redirecting to the active node inside the cluster is normal,
// a redirect to a foreign host collecting the token is not.
//
// The comparison is against via[0] because net/http rebuilds each redirected
// request from the original headers, so deleting on one hop does not stick.
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

// warnInsecure and warnCleartext are the vault-package copies of the warnings
// internal/auth emits: every path that disables verification or sends a
// credential in the clear says so, not just the Keystone one.
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
		"WARNING: %s is plain HTTP (%s); tokens and secret data are sent unencrypted\n", target, rawURL)
}
