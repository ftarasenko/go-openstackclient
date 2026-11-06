package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack/resolve"
)

// Scope is designate's per-request admin context.
type Scope struct {
	// AllProjects acts across every project (X-Auth-All-Projects).
	AllProjects bool
	// SudoProjectID acts as another project (X-Auth-Sudo-Project-ID).
	SudoProjectID string
}

// Headers returns the request headers s stands for, or nil for the zero
// Scope.
func (s Scope) Headers() map[string]string {
	if !s.AllProjects && s.SudoProjectID == "" {
		return nil
	}
	h := make(map[string]string, 2)
	if s.AllProjects {
		h["X-Auth-All-Projects"] = "true"
	}
	if s.SudoProjectID != "" {
		h["X-Auth-Sudo-Project-ID"] = s.SudoProjectID
	}
	return h
}

// WithScope returns a shallow copy of c that sends s's headers on every
// request, or c itself for the zero Scope.
func WithScope(c *gophercloud.ServiceClient, s Scope) *gophercloud.ServiceClient {
	h := s.Headers()
	if len(h) == 0 {
		return c
	}
	copied := *c
	copied.MoreHeaders = make(map[string]string, len(c.MoreHeaders)+len(h))
	maps.Copy(copied.MoreHeaders, c.MoreHeaders)
	maps.Copy(copied.MoreHeaders, h)
	return &copied
}

// wrap prefixes err with a formatted message; a nil err stays nil.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// withTrailingDot makes a name fully qualified, as designate stores it.
func withTrailingDot(name string) string {
	if name == "" || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// findID is the policy every Find…ID shares.
func findID[T any](ctx context.Context, kind, ref string,
	list func(context.Context) ([]T, error), idOf func(T) string,
) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	all, err := list(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, ref, err)
	}
	switch len(all) {
	case 1:
		return idOf(all[0]), nil
	case 0:
		return "", &resolve.NotFoundError{Kind: kind, Ref: ref}
	default:
		return "", &resolve.AmbiguousError{Kind: kind, Ref: ref, Count: len(all)}
	}
}

// Each raw helper closes the body itself: the close has to be visible in the
// function that made the request for the leak analysis (bodyclose) to see it.

func getJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, out any) error {
	resp, err := c.Get(ctx, url, out, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func postJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, body, out any) error {
	resp, err := c.Post(ctx, url, body, out, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK, http.StatusCreated, http.StatusAccepted},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func postNoContent(ctx context.Context, c *gophercloud.ServiceClient, url string, body any) error {
	resp, err := c.Post(ctx, url, body, nil, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func postRaw(ctx context.Context, c *gophercloud.ServiceClient, url, contentType string, body io.Reader, out any) error {
	resp, err := c.Post(ctx, url, body, out, &gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"Content-Type": contentType},
		OkCodes:     []int{http.StatusOK, http.StatusCreated, http.StatusAccepted},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// patchJSON sends a JSON PATCH. A nil value in body is sent as null, which
// designate reads as "clear this field".
func patchJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, body map[string]any, out any) error {
	resp, err := c.Request(ctx, http.MethodPatch, url, &gophercloud.RequestOpts{
		JSONBody:     body,
		JSONResponse: out,
		MoreHeaders:  map[string]string{"Content-Type": "application/json"},
		OkCodes:      []int{http.StatusOK, http.StatusAccepted},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func del(ctx context.Context, c *gophercloud.ServiceClient, url string, okCodes ...int) error {
	if len(okCodes) == 0 {
		okCodes = []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent}
	}
	resp, err := c.Delete(ctx, url, &gophercloud.RequestOpts{OkCodes: okCodes})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func getText(ctx context.Context, c *gophercloud.ServiceClient, url, accept string) (string, error) {
	resp, err := c.Get(ctx, url, nil, &gophercloud.RequestOpts{
		MoreHeaders:      map[string]string{"Accept": accept},
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

// links is designate's pagination block.
type links struct {
	Next string `json:"next"`
}

// listAll follows links.next from url and stops once limit items are in
// hand (0 = no cap). decode returns a page's items and its next link.
func listAll[T any](ctx context.Context, c *gophercloud.ServiceClient, url string, limit int,
	decode func(json.RawMessage) ([]T, string, error),
) ([]T, error) {
	var all []T
	for url != "" {
		var raw json.RawMessage
		if err := getJSON(ctx, c, url, &raw); err != nil {
			return nil, err
		}
		items, next, err := decode(raw)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if limit > 0 && len(all) >= limit {
			return all[:limit], nil
		}
		url = next
	}
	return all, nil
}

// query renders opts' q-tagged fields as a query string.
func query(opts any) (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return "", fmt.Errorf("building query: %w", err)
	}
	return q.String(), nil
}
