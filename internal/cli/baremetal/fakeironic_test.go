package baremetal

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// fakeIronic is a scripted ironic for the raw-request verbs: each route answers
// a fixed status and body, and every request is recorded with its decoded JSON
// body and microversion header, so a test asserts the exact wire exchange.
type fakeIronic struct {
	t      *testing.T
	routes map[string]fakeRoute
	calls  []fakeCall
	// max is the X-OpenStack-Ironic-API-Maximum-Version every answer carries.
	max string
}

type fakeRoute struct {
	status int
	body   any
}

type fakeCall struct {
	Method, Path, Query, Version string
	Body                         any
}

// newFakeIronic serves routes keyed "METHOD /path" (path relative to the
// endpoint, no query) and returns a client speaking microversion "latest".
func newFakeIronic(t *testing.T, routes map[string]fakeRoute) (*gophercloud.ServiceClient, *fakeIronic) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	f := &fakeIronic{t: t, routes: routes, max: "1.96"}
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		call := fakeCall{
			Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
			Version: r.Header.Get("X-OpenStack-Ironic-API-Version"),
		}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &call.Body); err != nil {
				t.Errorf("%s %s: request body is not JSON: %s", r.Method, r.URL, raw)
			}
		}
		f.calls = append(f.calls, call)
		route, ok := routes[r.Method+" "+call.Path]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set(ironicMaxVersionHeader, f.max)
		if route.body == nil {
			w.WriteHeader(route.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(route.status)
		_ = json.NewEncoder(w).Encode(route.body)
	})
	return baremetalClient(fakeServer, "latest"), f
}

// call returns the one recorded request matching "METHOD /path".
func (f *fakeIronic) call(key string) fakeCall {
	f.t.Helper()
	var found []fakeCall
	for _, c := range f.calls {
		if c.Method+" "+c.Path == key {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		f.t.Fatalf("%d requests for %s, want 1 (calls: %v)", len(found), key, f.calls)
	}
	if found[0].Version != "latest" {
		f.t.Errorf("%s sent X-OpenStack-Ironic-API-Version %q, want latest", key, found[0].Version)
	}
	return found[0]
}

// jsonEq reports whether got, once round-tripped, equals the JSON want.
func jsonEq(t *testing.T, what string, got any, want string) {
	t.Helper()
	g, _ := json.Marshal(got)
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want JSON for %s: %v", what, err)
	}
	wb, _ := json.Marshal(w)
	if string(g) != string(wb) {
		t.Errorf("%s =\n  %s\nwant\n  %s", what, g, wb)
	}
}

// decodeJSON unmarshals rendered -f json output.
func decodeJSON[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, s)
	}
	return v
}
