package openstack_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeCloud is a Keystone that issues one token, project-scoped to
// fakeProjectID, whose catalog lists image and network in two regions, and the
// services behind it, which answer every request with a version document.
type fakeCloud struct {
	*httptest.Server

	mu       sync.Mutex
	tokenReq map[string]any
}

const (
	fakeToken     = "fake-token-0123"
	fakeProjectID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func newFakeCloud(t *testing.T) *fakeCloud {
	t.Helper()
	fc := startFakeCloud()
	t.Cleanup(fc.Close)
	return fc
}

// startFakeCloud is newFakeCloud for the examples, which have no *testing.T;
// the caller closes it.
func startFakeCloud() *fakeCloud {
	fc := &fakeCloud{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/auth/tokens", fc.issueToken)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// gophercloud discovers a catalog endpoint's major version from its
		// root, so every answer carries a version document; list and show
		// responses simply ignore the extra key.
		_, _ = io.WriteString(w, `{"versions": [{"id": "v2.0", "status": "CURRENT"}]}`)
	})
	fc.Server = httptest.NewServer(mux)
	return fc
}

func (fc *fakeCloud) issueToken(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	fc.mu.Lock()
	fc.tokenReq = body
	fc.mu.Unlock()

	endpoint := func(region, path string) map[string]any {
		return map[string]any{"interface": "public", "region": region, "region_id": region, "url": fc.URL + path}
	}
	service := func(typ, name string) map[string]any {
		return map[string]any{"type": typ, "name": name, "endpoints": []any{
			endpoint("RegionOne", "/r1/"+typ+"/"),
			endpoint("RegionTwo", "/r2/"+typ+"/"),
		}}
	}
	w.Header().Set("X-Subject-Token", fakeToken)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{
		"expires_at": "2099-01-01T00:00:00.000000Z",
		"project":    map[string]any{"id": fakeProjectID, "name": "demo", "domain": map[string]any{"id": "default"}},
		"catalog": []any{
			service("image", "glance"),
			service("network", "neutron"),
			service("compute", "nova"),
			service("keyvrm", "keyvrm"),
		},
	}})
}
