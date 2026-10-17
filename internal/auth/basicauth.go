package auth

import (
	"net/http"
	"slices"
)

// basicAuthTransport injects HTTP Basic credentials onto every request bound for
// one of the configured hosts (the standalone ironic of --creds-from-ns, or the
// service endpoints of --os-auth-type http_basic).
//
// The host check is the security-relevant part: injecting at the RoundTripper
// runs on every request the client makes *including each redirected one*, which
// re-adds the credentials after net/http has stripped them for a cross-host hop.
// Restricting injection to the endpoint's own host means a 302 to somewhere else
// cannot collect the standalone ironic password.
type basicAuthTransport struct {
	base     http.RoundTripper
	hosts    []string // host:port of each endpoint; other hosts get no credentials
	username string
	password string
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.hosts) > 0 && !slices.Contains(t.hosts, req.URL.Host) {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.SetBasicAuth(t.username, t.password)
	return t.base.RoundTrip(r)
}
