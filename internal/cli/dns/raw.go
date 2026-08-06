package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"
)

// Raw designate fallbacks.
//
// gophercloud v2.13.0 ships dns/v2 packages for zones, recordsets, quotas,
// tsigkeys and zone transfers only. The rest of the designate v2 surface has no
// typed package at all, so the commands built on it go through the helpers below
// rather than each growing its own transport code. Per AGENTS.md the fallback is
// isolated here, and the endpoints are named where they are used:
//
//	zone export     POST /v2/zones/{zone}/tasks/export
//	                GET|DELETE /v2/zones/tasks/exports[/{id}]
//	                GET /v2/zones/tasks/exports/{id}/export   (Accept: text/dns)
//	zone import     POST /v2/zones/tasks/imports              (Content-Type: text/dns)
//	                GET|DELETE /v2/zones/tasks/imports[/{id}]
//	zone blacklist  POST|GET /v2/blacklists, GET|PATCH|DELETE /v2/blacklists/{id}
//	tld             POST|GET /v2/tlds, GET|PATCH|DELETE /v2/tlds/{id}
//
// Designate does not use microversions — v2 is selected by the URL, which the
// service client's Endpoint already carries — so there is no microversion to pin
// on these calls.

// dnsGetJSON performs a GET and decodes the JSON body into out.
func dnsGetJSON(ctx context.Context, client *gophercloud.ServiceClient, url string,
	headers map[string]string, out any,
) error {
	resp, err := client.Get(ctx, url, out, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{http.StatusOK},
	})
	// Each helper closes the body itself rather than delegating to a shared
	// closer: the close has to be visible in the function that made the request
	// for the leak analysis to see it.
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// dnsPostJSON performs a POST and decodes the JSON body into out. body may be nil
// for the task endpoints that take no payload (zone export create). 202 is
// accepted because designate answers its asynchronous task endpoints with it.
func dnsPostJSON(ctx context.Context, client *gophercloud.ServiceClient, url string,
	body, out any, headers map[string]string,
) error {
	resp, err := client.Post(ctx, url, body, out, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{http.StatusOK, http.StatusCreated, http.StatusAccepted},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// dnsPostNoContent performs a POST that answers with no body — designate's zone
// task endpoints, which reply 202 or 204 and nothing else. It must not ask
// gophercloud to decode a JSONResponse: an empty body is not valid JSON, so the
// decode would fail on a request that in fact succeeded.
func dnsPostNoContent(ctx context.Context, client *gophercloud.ServiceClient, url string,
	body any, headers map[string]string,
) error {
	resp, err := client.Post(ctx, url, body, nil, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// dnsPostRaw performs a POST whose body is not JSON — a BIND zonefile sent as
// text/dns, which is how designate accepts a bare zone import.
func dnsPostRaw(ctx context.Context, client *gophercloud.ServiceClient, url, contentType string,
	body io.Reader, out any, headers map[string]string,
) error {
	resp, err := client.Post(ctx, url, body, out, &gophercloud.RequestOpts{
		MoreHeaders: withHeader(headers, "Content-Type", contentType),
		OkCodes:     []int{http.StatusOK, http.StatusCreated, http.StatusAccepted},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// dnsPatchJSON performs the PATCH designate uses for every update. body is a map
// so a nil-valued key ("clear the description") survives serialisation, which an
// omitempty-tagged struct field cannot express. ServiceClient has no Patch
// shorthand, hence the explicit Request.
func dnsPatchJSON(ctx context.Context, client *gophercloud.ServiceClient, url string,
	body map[string]any, out any, headers map[string]string,
) error {
	resp, err := client.Request(ctx, http.MethodPatch, url, &gophercloud.RequestOpts{
		JSONBody:     body,
		JSONResponse: out,
		MoreHeaders:  withHeader(headers, "Content-Type", "application/json"),
		OkCodes:      []int{http.StatusOK, http.StatusAccepted},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// dnsDelete performs a DELETE. 202 is accepted for the resources designate
// removes asynchronously (zone exports and imports).
func dnsDelete(ctx context.Context, client *gophercloud.ServiceClient, url string,
	headers map[string]string,
) error {
	resp, err := client.Delete(ctx, url, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// dnsGetText performs a GET that returns something other than JSON — the zone
// export file, which designate serves as a BIND zonefile under Accept: text/dns.
func dnsGetText(ctx context.Context, client *gophercloud.ServiceClient, url, accept string,
	headers map[string]string,
) (string, error) {
	// With no JSONResponse gophercloud would close the body, so ask to keep it and
	// read it here.
	resp, err := client.Get(ctx, url, nil, &gophercloud.RequestOpts{
		MoreHeaders:      withHeader(headers, "Accept", accept),
		OkCodes:          []int{http.StatusOK},
		KeepResponseBody: true,
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// cloneHeaders copies a header map so callers never mutate one that is shared
// across the requests of a single invocation.
func cloneHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	return out
}

// withHeader returns headers plus one more entry, leaving the input untouched.
func withHeader(headers map[string]string, key, value string) map[string]string {
	out := cloneHeaders(headers)
	out[key] = value
	return out
}

// dnsListAll walks every page of a designate collection. Designate paginates with
// a "links.next" absolute URL rather than a marker the caller computes, so decode
// returns the items on the page plus that URL, and an empty string ends the walk.
// It stands in for the gophercloud Pager these resources have no package for.
func dnsListAll[T any](ctx context.Context, client *gophercloud.ServiceClient, url string,
	headers map[string]string, limit int,
	decode func(json.RawMessage) ([]T, string, error),
) ([]T, error) {
	var all []T
	for url != "" {
		var raw json.RawMessage
		if err := dnsGetJSON(ctx, client, url, headers, &raw); err != nil {
			return nil, err
		}
		items, next, err := decode(raw)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		// --limit is a hard cap on results, not just a page size (AGENTS.md), so
		// stop fetching as soon as it is reached.
		if limit > 0 && len(all) >= limit {
			return all[:limit], nil
		}
		url = next
	}
	return all, nil
}

// dnsLinks is the "links" object every designate collection carries; only "next"
// matters for walking pages.
type dnsLinks struct {
	Next string `json:"next"`
}

// dnsQuery renders opts as a query string to append to a raw URL, so the raw
// commands filter server-side exactly as the typed ones do.
func dnsQuery(opts any) (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return "", fmt.Errorf("building query: %w", err)
	}
	return q.String(), nil
}

// commonOptions are designate's cross-cutting CLI options
// (designateclient/v2/cli/common.py). Both travel as request headers, not query
// parameters, and both need an admin role server-side.
type commonOptions struct {
	allProjects   bool
	sudoProjectID string
}

// bind registers --all-projects/--sudo-project-id on a command; upstream accepts
// them on every designate verb, write ones included.
func (c *commonOptions) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVar(&c.allProjects, "all-projects", false, "act across all projects (admin)")
	fl.StringVar(&c.sudoProjectID, "sudo-project-id", "", "project ID to impersonate for this command (admin)")
}

// headers renders the options as the headers designate reads them from, and nil
// when neither was given so an ordinary call sends no extra headers at all.
func (c *commonOptions) headers() map[string]string {
	if c == nil || (!c.allProjects && c.sudoProjectID == "") {
		return nil
	}
	h := make(map[string]string, 2)
	if c.allProjects {
		h["X-Auth-All-Projects"] = "true"
	}
	if c.sudoProjectID != "" {
		h["X-Auth-Sudo-Project-ID"] = c.sudoProjectID
	}
	return h
}

// withCommonHeaders returns a shallow copy of the service client carrying the
// common headers, for commands that reach designate through a *typed* gophercloud
// package and so cannot pass RequestOpts of their own. Copying rather than
// mutating matters: one invocation shares the client, and MoreHeaders is read on
// every request it makes.
func withCommonHeaders(client *gophercloud.ServiceClient, c *commonOptions) *gophercloud.ServiceClient {
	h := c.headers()
	if len(h) == 0 {
		return client
	}
	copied := *client
	copied.MoreHeaders = cloneHeaders(h)
	return &copied
}
