package vaulttest_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/vault/vaulttest"
)

// TestContract pins the fake to the OpenBao 2.7.0 probe, row by row: the status
// and the body a real server gave for each case (see the package doc). A change
// here must come from a fresh probe, never from what koc happens to want.
//
// The same table re-runs against a real server, which is how the probe is
// repeated rather than re-typed:
//
//	bao server -dev -dev-root-token-id=root &   # or vault server -dev
//	VAULTTEST_ADDR=http://127.0.0.1:8200 go test ./internal/vault/vaulttest -run TestContract
//
// It seeds that server through its own API with the state the fake gets from
// Put/SoftDelete/AddAppRole/AddToken. Use a throwaway dev server: it enables a
// mount, an auth method and a namespace there.
func TestContract(t *testing.T) {
	addr := contractTarget(t)

	for _, tc := range []struct {
		name       string
		method     string
		path       string
		token      string
		namespace  string
		body       string
		wantStatus int
		wantBody   string // a JSON fragment the body must contain
	}{
		{name: "read latest", method: "GET", path: "/v1/secret_v2/data/a/b", token: "root",
			wantStatus: 200, wantBody: `"data":{"k":"v2"}`},
		{name: "read version 1", method: "GET", path: "/v1/secret_v2/data/a/b?version=1", token: "root",
			wantStatus: 200, wantBody: `"data":{"k":"v1"}`},
		{name: "read version past current", method: "GET", path: "/v1/secret_v2/data/a/b?version=9", token: "root",
			wantStatus: 404, wantBody: `{"errors":[]}`},
		{name: "read missing secret", method: "GET", path: "/v1/secret_v2/data/zzz", token: "root",
			wantStatus: 404, wantBody: `{"errors":[]}`},
		{name: "read soft-deleted latest", method: "GET", path: "/v1/secret_v2/data/gone", token: "root",
			wantStatus: 404, wantBody: `"data":null`},
		{name: "metadata of existing secret", method: "GET", path: "/v1/secret_v2/metadata/a/b", token: "root",
			wantStatus: 200, wantBody: `"current_version":2`},
		{name: "metadata of missing secret", method: "GET", path: "/v1/secret_v2/metadata/nope", token: "root",
			wantStatus: 404, wantBody: `{"errors":[]}`},
		{name: "list root, folder keeps its slash", method: "GET", path: "/v1/secret_v2/metadata/?list=true", token: "root",
			wantStatus: 200, wantBody: `"keys":["a/","gone"]`},
		{name: "LIST verb", method: "LIST", path: "/v1/secret_v2/metadata/a", token: "root",
			wantStatus: 200, wantBody: `"keys":["b"]`},
		{name: "list with trailing slash", method: "GET", path: "/v1/secret_v2/metadata/a/?list=true", token: "root",
			wantStatus: 200, wantBody: `"keys":["b"]`},
		{name: "list empty or missing path", method: "GET", path: "/v1/secret_v2/metadata/nope?list=true", token: "root",
			wantStatus: 404, wantBody: `{"errors":[]}`},
		{name: "list a leaf secret", method: "GET", path: "/v1/secret_v2/metadata/a/b?list=true", token: "root",
			wantStatus: 404, wantBody: `{"errors":[]}`},
		{name: "write returns the new version", method: "POST", path: "/v1/secret_v2/data/new", token: "root",
			body: `{"data":{"k":"v"}}`, wantStatus: 200, wantBody: `"version":1`},
		{name: "no token", method: "GET", path: "/v1/secret_v2/data/a/b",
			wantStatus: 403, wantBody: `{"errors":["permission denied"]}`},
		{name: "unknown token", method: "GET", path: "/v1/secret_v2/data/a/b", token: "nope",
			wantStatus: 403, wantBody: `{"errors":["permission denied"]}`},
		{name: "write without the capability", method: "POST", path: "/v1/secret_v2/data/a/b", token: "reader",
			body: `{"data":{"k":"x"}}`, wantStatus: 403, wantBody: `{"errors":["1 error occurred:\n\t* permission denied\n\n"]}`},
		{name: "approle bad secret_id", method: "POST", path: "/v1/auth/approle/login",
			body: `{"role_id":"role-1","secret_id":"x"}`, wantStatus: 400, wantBody: `{"errors":["invalid role or secret ID"]}`},
		{name: "approle missing role_id", method: "POST", path: "/v1/auth/approle/login",
			body: `{"secret_id":"x"}`, wantStatus: 500, wantBody: `{"errors":["missing role_id"]}`},
		{name: "approle login", method: "POST", path: "/v1/auth/approle/login",
			body: `{"role_id":"role-1","secret_id":"secret-1"}`, wantStatus: 200, wantBody: `"token_type":"service"`},
		{name: "unknown namespace", method: "GET", path: "/v1/secret_v2/data/a/b", token: "root", namespace: "nosuch",
			wantStatus: 404, wantBody: `{"errors":["namespace not found"]}`},
		{name: "known namespace has its own secrets", method: "GET", path: "/v1/secret_v2/data/a/b", token: "root", namespace: "team",
			wantStatus: 404, wantBody: `{"errors":[]}`},
		{name: "unknown mount", method: "GET", path: "/v1/nosuch/data/x", token: "root",
			wantStatus: 404, wantBody: `{"errors":["no handler for route \"nosuch/data/x\". route entry not found."]}`},
		{name: "kv v1 mount through the v2 data API", method: "GET", path: "/v1/kv1/data/x", token: "root",
			wantStatus: 404, wantBody: `{"errors":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tc.method, addr+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.token != "" {
				req.Header.Set("X-Vault-Token", tc.token)
			}
			if tc.namespace != "" {
				req.Header.Set("X-Vault-Namespace", tc.namespace)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", resp.StatusCode, tc.wantStatus, raw)
			}
			// Compare compactly: the fake's encoder and the probe's bodies
			// differ only in whitespace and key order.
			if got := compact(t, raw); !strings.Contains(got, tc.wantBody) {
				t.Errorf("body = %s, want it to contain %s", got, tc.wantBody)
			}
		})
	}
}

// compact re-encodes a JSON body with sorted keys and no insignificant
// whitespace, so a fragment check does not depend on either.
func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("response is not JSON: %s", raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// contractTarget returns the base address TestContract runs against: a fake
// seeded in-process, or — with VAULTTEST_ADDR — a real dev server seeded over
// HTTP with the same state.
func contractTarget(t *testing.T) string {
	t.Helper()
	if addr := os.Getenv("VAULTTEST_ADDR"); addr != "" {
		seedReal(t, strings.TrimRight(addr, "/"))
		return strings.TrimRight(addr, "/")
	}
	s := vaulttest.New(t, vaulttest.WithKVv1Mount("kv1"), vaulttest.WithNamespace("team"))
	s.Put("", "secret_v2", "a/b", map[string]any{"k": "v1"})
	s.Put("", "secret_v2", "a/b", map[string]any{"k": "v2"})
	s.Put("", "secret_v2", "gone", map[string]any{"k": "x"})
	s.SoftDelete("", "secret_v2", "gone")
	s.AddAppRole("role-1", "secret-1", vaulttest.ReadOnly)
	s.AddToken("reader", vaulttest.ReadOnly)
	return s.URL
}

// seedReal gives a real dev server (root token "root") the state the fake is
// seeded with above. A mount or method that already exists answers 400, which
// is ignored so the seed can be re-run.
func seedReal(t *testing.T, addr string) {
	t.Helper()
	call := func(ns, method, path, body string, mustSucceed bool) {
		req, err := http.NewRequestWithContext(t.Context(), method, addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Vault-Token", "root")
		if ns != "" {
			req.Header.Set("X-Vault-Namespace", ns)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("seeding %s %s: %v", method, path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if mustSucceed && resp.StatusCode >= 300 {
			t.Fatalf("seeding %s %s: %d %s", method, path, resp.StatusCode, raw)
		}
	}
	call("", "POST", "/v1/sys/namespaces/team", `{}`, false)
	for _, ns := range []string{"", "team"} {
		call(ns, "POST", "/v1/sys/mounts/secret_v2", `{"type":"kv","options":{"version":"2"}}`, false)
	}
	call("", "POST", "/v1/sys/mounts/kv1", `{"type":"kv","options":{"version":"1"}}`, false)
	call("", "POST", "/v1/secret_v2/data/a/b", `{"data":{"k":"v1"}}`, true)
	call("", "POST", "/v1/secret_v2/data/a/b", `{"data":{"k":"v2"}}`, true)
	call("", "POST", "/v1/secret_v2/data/gone", `{"data":{"k":"x"}}`, true)
	call("", "DELETE", "/v1/secret_v2/data/gone", ``, true)
	call("", "PUT", "/v1/sys/policies/acl/koc-read", `{"policy":"path \"secret_v2/*\" { capabilities = [\"read\", \"list\"] }"}`, true)
	call("", "POST", "/v1/sys/auth/approle", `{"type":"approle"}`, false)
	call("", "POST", "/v1/auth/approle/role/koc", `{"token_policies":"koc-read"}`, true)
	call("", "POST", "/v1/auth/approle/role/koc/role-id", `{"role_id":"role-1"}`, true)
	call("", "POST", "/v1/auth/approle/role/koc/custom-secret-id", `{"secret_id":"secret-1"}`, false)
	call("", "POST", "/v1/auth/token/create", `{"id":"reader","policies":["koc-read"]}`, false)
}
