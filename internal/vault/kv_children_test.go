package vault

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// listKVChildren is the half of WalkKV that talks to vault: one listing, folder
// entries separated from leaves, and every key validated before it is handed to
// a caller that will join it onto a path.
func TestListKVChildren(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		want    []kvChild
		wantErr string
	}{
		{
			name: "leaves and folders are told apart by the trailing slash",
			body: `{"data":{"keys":["b-secret","sub/","a-secret"]}}`,
			want: []kvChild{
				{name: "b-secret"},
				{name: "sub", folder: true},
				{name: "a-secret"},
			},
		},
		{
			// A subfolder that vanished mid-walk must not fail the whole walk.
			name:   "a 404 listing yields no children, not an error",
			status: http.StatusNotFound,
			body:   `{"errors":[]}`,
		},
		{
			name: "an empty listing yields no children",
			body: `{"data":{"keys":[]}}`,
			want: []kvChild{},
		},
		{
			name: "empty keys are dropped",
			body: `{"data":{"keys":["","ok"]}}`,
			want: []kvChild{{name: "ok"}},
		},
		{
			// The listing is the server's data and callers join it onto a
			// destination path, so a traversing key fails here rather than being
			// carried along.
			name:    "a traversing key fails the listing",
			body:    `{"data":{"keys":["ok","../../../prod/openrc"]}}`,
			wantErr: "returned an unusable key",
		},
		{
			name:    "a key that could inject a query fails the listing",
			body:    `{"data":{"keys":["openrc?list=true"]}}`,
			wantErr: "returned an unusable key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.listKVChildren(context.Background(), "kv", "root")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("listKVChildren() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("listKVChildren() error = %v", err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("listKVChildren() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A listing that fails for any reason other than "not found" still fails the
// walk: a 403 on one subfolder would otherwise silently shorten the result.
func TestListKVChildren_PropagatesRealErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.listKVChildren(context.Background(), "kv", "root"); err == nil {
		t.Fatal("listKVChildren() error = nil, want the 403 to propagate")
	}
}

// The listing must be requested as a LIST of the metadata endpoint.
func TestListKVChildren_RequestShape(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":{"keys":["leaf"]}}`))
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.listKVChildren(context.Background(), "kv", "root/sub"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET (vault's LIST is GET ?list=true)", gotMethod)
	}
	if gotPath != "/v1/kv/metadata/root/sub" {
		t.Errorf("path = %q, want /v1/kv/metadata/root/sub", gotPath)
	}
	if gotQuery != "list=true" {
		t.Errorf("query = %q, want list=true", gotQuery)
	}
}
