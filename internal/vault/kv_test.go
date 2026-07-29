package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestResolvePath(t *testing.T) {
	const prefix = "deployments/itkey/e2e-lcm/reg"
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
		{prefix, "/secret_v2/deployments/itkey/dev/x/openrc", "deployments/itkey/dev/x/openrc"},
		{prefix, "secret_v2/deployments/itkey/dev/x/openrc", "deployments/itkey/dev/x/openrc"},
	}
	for _, c := range cases {
		if got := ResolvePath(c.prefix, mount, c.arg); got != c.want {
			t.Errorf("ResolvePath(%q,%q,%q) = %q, want %q", c.prefix, mount, c.arg, got, c.want)
		}
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
