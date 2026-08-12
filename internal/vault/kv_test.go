package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	const prefix = "deployments/example/dev/reg"
	const mount = "secret_v2"
	full := prefix + "/reg-cp/openrc"
	cases := []struct {
		prefix, arg, want string
	}{
		{prefix, "reg-cp/openrc", full},                   // relative → joined
		{prefix, full, full},                              // already full → unchanged
		{prefix, "/other/abs/openrc", "other/abs/openrc"}, // leading / → absolute
		{"", "a/b/openrc", "a/b/openrc"},                  // no prefix
		{prefix, prefix, prefix},                          // equal to prefix
		{"/reg/", "reg-cp/openrc", "reg/reg-cp/openrc"},   // prefix slashes trimmed
		// Vault "mount/path" form: leading mount is stripped, rest is absolute.
		{prefix, "/secret_v2/deployments/example/other/x/openrc", "deployments/example/other/x/openrc"},
		{prefix, "secret_v2/deployments/example/other/x/openrc", "deployments/example/other/x/openrc"},
	}
	for _, c := range cases {
		if got := ResolvePath(c.prefix, mount, c.arg); got != c.want {
			t.Errorf("ResolvePath(%q,%q,%q) = %q, want %q", c.prefix, mount, c.arg, got, c.want)
		}
	}
}

// kvPath used to concatenate raw segments, so a secret name could steer the
// request instead of naming it: "?" started a query string and "../" normalised
// out of the mount.
func TestKVPath_EscapesSegments(t *testing.T) {
	cases := []struct{ mount, api, path, want string }{
		{"secret_v2", "metadata", "deployments/dev", "/v1/secret_v2/metadata/deployments/dev"},
		{"secret_v2", "data", "", "/v1/secret_v2/data"},
		{"secret_v2", "data", "a?list=true", "/v1/secret_v2/data/a%3Flist=true"},
		{"secret_v2", "data", "a#frag", "/v1/secret_v2/data/a%23frag"},
		{"secret_v2", "data", "a/../../b", "/v1/secret_v2/data/a/../../b"},
		{"weird mount", "data", "x", "/v1/weird%20mount/data/x"},
	}
	for _, c := range cases {
		if got := kvPath(c.mount, c.api, c.path); got != c.want {
			t.Errorf("kvPath(%q,%q,%q) = %q, want %q", c.mount, c.api, c.path, got, c.want)
		}
	}
}

// End to end: a "?" in a path must reach the server as part of the path, never as
// a query parameter that changes what the request means.
func TestReadKVDataAt_QueryInjection(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":{"data":{"k":"v"}}}`))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	if _, err := c.ReadKVDataAt(context.Background(), "kv", "openrc?list=true", 0); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/kv/data/openrc?list=true" {
		t.Errorf("path = %q, want the ? inside the path segment", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty: the path must not inject parameters", gotQuery)
	}
}

func TestValidateRelPath(t *testing.T) {
	valid := []string{"", "openrc", "nested/accounts", "a-b_c.1/x"}
	for _, rel := range valid {
		if err := ValidateRelPath(rel); err != nil {
			t.Errorf("ValidateRelPath(%q) = %v, want nil", rel, err)
		}
	}
	invalid := []string{
		"../../../prod/openrc", // the write-escape case
		"..",
		"a/../b",
		"/absolute",
		"a//b",
		"a?list=true",
		"a#frag",
	}
	for _, rel := range invalid {
		if err := ValidateRelPath(rel); err == nil {
			t.Errorf("ValidateRelPath(%q) = nil, want an error", rel)
		}
	}
}

// A hostile or spoofed source Vault answering a LIST with a traversing key must
// fail the walk, not hand the key to a caller that will join it onto a
// destination path.
func TestWalkKV_RejectsTraversingKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/kv/metadata/root" {
			_, _ = w.Write([]byte(`{"data":{"keys":["ok","../../../prod/openrc"]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[]}`))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	_, err := c.WalkKV(context.Background(), "kv", "root")
	if err == nil {
		t.Fatal("WalkKV should reject a traversing key")
	}
	if !strings.Contains(err.Error(), "prod/openrc") {
		t.Errorf("err = %v, want it to name the offending key", err)
	}
}

func TestListKV(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":{"keys":["openrc","nested/"]}}`))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t", KVMount: "secret_v2"})
	keys, err := c.ListKV(context.Background(), "secret_v2", "deployments/dev")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/secret_v2/metadata/deployments/dev" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "list=true" {
		t.Errorf("query = %q, want list=true", gotQuery)
	}
	if want := []string{"openrc", "nested/"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestWriteKVData(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"data":{"version":3}}`))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	err := c.WriteKVData(context.Background(), "dst_kv", "a/b", map[string]any{"user": "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/dst_kv/data/a/b" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["data"]["user"] != "admin" {
		t.Errorf("body = %v, want the secret nested under \"data\"", gotBody)
	}
}

func TestReadKVDataAt_Version(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":{"data":{"k":"v"}}}`))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	if _, err := c.ReadKVDataAt(context.Background(), "m", "p", 4); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "version=4" {
		t.Errorf("query = %q, want version=4", gotQuery)
	}
}

// TestWalkKV covers the case that breaks vault-helper.py's one-level copy: a
// folder entry among the keys must be descended into, not read as a secret. The
// "empty" subfolder also asserts a 404 mid-walk is tolerated.
func TestWalkKV(t *testing.T) {
	listings := map[string]string{
		"/v1/kv/metadata/root":               `{"data":{"keys":["b-secret","sub/","empty/"]}}`,
		"/v1/kv/metadata/root/sub":           `{"data":{"keys":["deep/","a-secret"]}}`,
		"/v1/kv/metadata/root/sub/deep":      `{"data":{"keys":["leaf"]}}`,
		"/v1/kv/metadata/root/sub/deep/x":    ``, // unused; guards against over-walking
		"/v1/kv/metadata/root/sub/deep/leaf": ``,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := listings[r.URL.Path]
		if !ok || body == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	got, err := c.WalkKV(context.Background(), "kv", "root")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b-secret", "sub/a-secret", "sub/deep/leaf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WalkKV = %v, want %v (sorted, relative to root)", got, want)
	}
}

func TestHasKVAndNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/kv/metadata/present" {
			_, _ = w.Write([]byte(`{"data":{"current_version":1}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[]}`))
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	ctx := context.Background()

	if ok, err := c.HasKV(ctx, "kv", "present"); err != nil || !ok {
		t.Errorf("HasKV(present) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := c.HasKV(ctx, "kv", "absent"); err != nil || ok {
		t.Errorf("HasKV(absent) = %v, %v; want false, nil", ok, err)
	}
	if _, err := c.ReadKVDataAt(ctx, "kv", "absent", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("read of a missing secret = %v, want ErrNotFound", err)
	}
}
