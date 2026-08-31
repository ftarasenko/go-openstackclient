package s3

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"
)

// HTTP transport policy for the S3 client. It mirrors the ones in internal/auth,
// internal/vault and internal/kube; the copies exist because those packages
// cannot import each other without a cycle. Keep them in sync.
const (
	// responseHeaderTimeout bounds "connected but silent" independently of the
	// whole-exchange timeout, which object transfers leave unbounded.
	responseHeaderTimeout = 60 * time.Second

	// maxRedirects caps a redirect chain. See sameHostRedirect for what travels
	// with one.
	maxRedirects = 5
)

// credentialHeaders are dropped when a redirect leaves the origin host. Go's own
// stripping covers only Authorization/Cookie/Www-Authenticate, which misses
// every x-amz-* header the signature is computed over.
var credentialHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"X-Amz-Date",
	"X-Amz-Content-Sha256",
	"X-Amz-Security-Token",
}

// newHTTPClient builds the S3 client's http.Client: a clone of
// http.DefaultTransport (so HTTPS_PROXY/NO_PROXY, the dial timeout,
// TLSHandshakeTimeout, IdleConnTimeout and HTTP/2 all keep working — a
// hand-rolled &http.Transport{} loses every one of them) with the caller's TLS
// config, a request timeout, and the redirect policy below.
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

// sameHostRedirect follows a redirect that stays on the origin host with the
// signature intact, and one that leaves it without any credential header.
//
// Unlike the Vault copy this tolerates a change of scheme, because it is the
// common case rather than an attack: a gateway fronting the S3 API 301s plain
// HTTP to HTTPS (KeyStack's garage-http-redirect route does exactly that), and
// SigV4 signs the Host header but not the scheme, so the upgraded request still
// verifies. A redirect to a *different* host invalidates the signature anyway —
// stripping the headers there just makes the failure an honest AccessDenied
// instead of leaking a credential to whoever answered.
//
// The comparison is against via[0] because net/http rebuilds each redirected
// request from the original headers, so deleting on one hop does not stick.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Host == via[0].URL.Host {
		return nil
	}
	for _, h := range credentialHeaders {
		req.Header.Del(h)
	}
	return nil
}

// warnInsecure and warnCleartext are this package's copies of the warnings
// internal/auth and internal/vault emit: every path that disables verification
// or sends a credential in the clear says so.
func warnInsecure(target string) {
	fmt.Fprintf(os.Stderr,
		"WARNING: TLS certificate verification is disabled for %s; connections are not secure\n", target)
}

func warnCleartext(rawURL, host string) {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return
	}
	fmt.Fprintf(os.Stderr,
		"WARNING: the S3 endpoint is plain HTTP (%s); the request signature and object data are sent unencrypted\n", rawURL)
}
