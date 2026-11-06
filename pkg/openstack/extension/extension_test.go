package extension_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack/extension"
)

// fake answers "GET /path" from routes, 404 otherwise.
func fake(t *testing.T, routes map[string]string) *gophercloud.ServiceClient {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		out, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"NeutronError":{"message":"not found"}}`)
			return
		}
		_, _ = io.WriteString(w, out)
	})
	return fakeclient.ServiceClient(fs)
}

func TestList(t *testing.T) {
	sc := fake(t, map[string]string{
		"GET /extensions": `{"extensions":[
			{"name":"Quota management support","alias":"quotas","description":"Expose quotas","updated":"2012-07-29T10:00:00-00:00","links":[]},
			{"name":"Trunk","alias":"trunk","description":"Trunks"}]}`,
	})
	got, err := extension.List(context.Background(), sc)
	if err != nil || len(got) != 2 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if got[0].Alias != "quotas" || got[0].Updated != "2012-07-29T10:00:00-00:00" || got[0].Links == nil {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Namespace != "" || got[1].Links != nil {
		t.Errorf("second = %+v; want the absent fields empty", got[1])
	}
}

func TestGet(t *testing.T) {
	sc := fake(t, map[string]string{
		"GET /extensions/trunk": `{"extension":{"name":"Trunk","alias":"trunk","description":"Trunks"}}`,
	})
	ctx := context.Background()
	if e, err := extension.Get(ctx, sc, "trunk"); err != nil || e.Name != "Trunk" {
		t.Fatalf("Get = %+v, %v", e, err)
	}
	_, err := extension.Get(ctx, sc, "nope")
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) || !strings.HasPrefix(err.Error(), `showing extension "nope": `) {
		t.Errorf("Get(nope) = %v; want the 404, named", err)
	}
	_, err = extension.List(ctx, sc)
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) || !strings.HasPrefix(err.Error(), "listing extensions: ") {
		t.Errorf("List = %v; want the 404, named", err)
	}
}
