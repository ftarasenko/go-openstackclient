package baremetal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// Raw ironic fallbacks.
//
// gophercloud v2.15.0 ships baremetal/v1 packages for nodes, ports, drivers,
// conductors and allocations only. Every other ironic resource koc exposes —
// port groups, deploy templates, runbooks, inspection rules, volume connectors
// and targets, shards — and the node sub-resources gophercloud does not model
// (traits, console, history, passthru, children, vmedia, secure boot) go
// through these helpers. The service client already carries the ironic
// microversion, so every call here is versioned like a typed one.

// ironicGet GETs path (relative to the ironic endpoint) into out.
func ironicGet(ctx context.Context, client *gophercloud.ServiceClient, path string, out any) error {
	resp, err := client.Get(ctx, client.ServiceURL(path), out, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// ironicPost POSTs body to path; out may be nil for an endpoint that answers
// with no body.
func ironicPost(ctx context.Context, client *gophercloud.ServiceClient, path string, body, out any) error {
	resp, err := client.Post(ctx, client.ServiceURL(path), body, out, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// ironicPut PUTs body to path; body may be nil.
func ironicPut(ctx context.Context, client *gophercloud.ServiceClient, path string, body any) error {
	opts := &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent},
	}
	if body != nil {
		opts.JSONBody = body
	}
	resp, err := client.Request(ctx, http.MethodPut, client.ServiceURL(path), opts)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// ironicPatch applies a JSON patch to the resource at path and decodes the
// updated resource into out.
func ironicPatch(ctx context.Context, client *gophercloud.ServiceClient, path string, ops []patchOp, out any) error {
	resp, err := client.Patch(ctx, client.ServiceURL(path), ops, out, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// ironicDelete DELETEs path; query may be empty.
func ironicDelete(ctx context.Context, client *gophercloud.ServiceClient, path string) error {
	resp, err := client.Delete(ctx, client.ServiceURL(path), &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// patchOp is one RFC 6902 operation, the body of every ironic update.
type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// patchBuilder accumulates the operations of a set/unset verb.
type patchBuilder []patchOp

func (b *patchBuilder) add(path string, v any) {
	*b = append(*b, patchOp{Op: "add", Path: path, Value: v})
}

func (b *patchBuilder) replace(path string, v any) {
	*b = append(*b, patchOp{Op: "replace", Path: path, Value: v})
}

func (b *patchBuilder) remove(path string) {
	*b = append(*b, patchOp{Op: "remove", Path: path})
}

// addKV adds a key=value pair under prefix ("/extra") for each of pairs.
func (b *patchBuilder) addKV(prefix, flag string, pairs []string) error {
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return fmt.Errorf("parsing --%s: %w", flag, err)
		}
		b.add(prefix+"/"+escapeJSONPointer(k), v)
	}
	return nil
}

// removeKeys removes each key under prefix.
func (b *patchBuilder) removeKeys(prefix string, keys []string) {
	for _, k := range keys {
		b.remove(prefix + "/" + escapeJSONPointer(k))
	}
}

// ironicPage is one page of an ironic collection: the items under the
// collection key, and a top-level "next" URL while more remain.
type ironicPage struct {
	pagination.LinkedPageBase
	key string
}

func (p ironicPage) NextPageURL() (string, error) {
	var s struct {
		Next string `json:"next"`
	}
	if err := p.ExtractInto(&s); err != nil {
		return "", err
	}
	return s.Next, nil
}

func (p ironicPage) IsEmpty() (bool, error) {
	if p.StatusCode == http.StatusNoContent {
		return true, nil
	}
	items, err := pageMaps(p.key)(p)
	return len(items) == 0, err
}

// ironicList walks every page of the collection at path (e.g. "portgroups" or
// "portgroups/detail"), whose items are under key. limit caps the result and
// is sent as the page size, as gophercloud's typed listings do.
func ironicList(ctx context.Context, client *gophercloud.ServiceClient, path, key string,
	query url.Values, limit int,
) ([]map[string]any, error) {
	q := url.Values{}
	for k, v := range query {
		for _, s := range v {
			if s != "" {
				q.Add(k, s)
			}
		}
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	u := client.ServiceURL(path)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	pager := pagination.NewPager(client, u, func(r pagination.PageResult) pagination.Page {
		return ironicPage{LinkedPageBase: pagination.LinkedPageBase{PageResult: r}, key: key}
	})
	return paging.Collect(ctx, pager, limit, pageMaps(key))
}

// joinPath joins resource path segments, escaping each one, so a name such as
// "CUSTOM_A/B" is one segment.
func joinPath(segments ...string) string {
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
