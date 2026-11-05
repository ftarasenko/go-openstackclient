package dns_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// routes serves fixed bodies by request path and counts every request.
func routes(t *testing.T, bodies map[string]string) (*gophercloud.ServiceClient, *int, string) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	calls := 0
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		calls++
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		body, ok := bodies[key]
		w.Header().Set("Content-Type", "application/json")
		switch {
		case !ok:
			t.Errorf("unexpected %s %s", r.Method, key)
			w.WriteHeader(http.StatusNotFound)
		case body == "500":
			w.WriteHeader(http.StatusInternalServerError)
			body = `{}`
		}
		_, _ = io.WriteString(w, body)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "dns"
	sc.ResourceBase = sc.Endpoint + "v2/"
	return sc, &calls, fs.Server.URL
}

func TestFindZone(t *testing.T) {
	list := `{"zones":[{"id":"` + zoneID + `","name":"example.com."},{"id":"` + objID + `","name":"example.org."}],"links":{}}`
	c, _, _ := routes(t, map[string]string{"/v2/zones": list})
	ctx := context.Background()
	for _, ref := range []string{zoneID, "example.com.", "example.com"} {
		if id, err := dns.FindZoneID(ctx, c, ref); err != nil || id != zoneID {
			t.Errorf("FindZoneID(%q) = %q, %v; want %s", ref, id, err, zoneID)
		}
	}
	_, err := dns.FindZone(ctx, c, "example.net")
	var nf *resolve.NotFoundError
	if !errors.As(err, &nf) || nf.Kind != "zone" || nf.Ref != "example.net" {
		t.Errorf("FindZone(missing) error = %v; want a zone NotFoundError", err)
	}

	failing, _, _ := routes(t, map[string]string{"/v2/zones": "500"})
	if _, err := dns.FindZoneID(ctx, failing, "example.com"); err == nil ||
		!strings.HasPrefix(err.Error(), `looking up zone "example.com": `) {
		t.Errorf("FindZoneID(500) error = %v", err)
	}
}

// Every Find…ID passes a UUID through without a request, takes the only
// match, and errors on none or several: the strict policy.
func TestFindIDs(t *testing.T) {
	tests := []struct {
		kind, path, key string
		find            func(context.Context, *gophercloud.ServiceClient, string) (string, error)
	}{
		{"DNS pool", "/v2/pools?name=web", "pools", dns.FindPoolID},
		{"TSIG key", "/v2/tsigkeys?name=web", "tsigkeys", dns.FindTSIGKeyID},
		{"blacklist", "/v2/blacklists?pattern=web", "blacklists", dns.FindBlacklistID},
		{"TLD", "/v2/tlds?name=web", "tlds", dns.FindTLDID},
		{"recordset", "/v2/zones/" + zoneID + "/recordsets?name=web.", "recordsets",
			func(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
				return dns.FindRecordSetID(ctx, c, zoneID, ref)
			}},
	}
	item := func(id string) string { return `{"id":"` + id + `","name":"web."}` }
	page := func(items ...string) string {
		return `{"` + "%s" + `":[` + strings.Join(items, ",") + `],"links":{}}`
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			body := func(items ...string) map[string]string {
				return map[string]string{tt.path: strings.Replace(page(items...), "%s", tt.key, 1)}
			}

			c, calls, _ := routes(t, body())
			if id, err := tt.find(ctx, c, objID); err != nil || id != objID || *calls != 0 {
				t.Errorf("UUID: %q, %v after %d requests; want it unchanged without a request", id, err, *calls)
			}

			c, _, _ = routes(t, body(item(objID)))
			if id, err := tt.find(ctx, c, "web"); err != nil || id != objID {
				t.Errorf("one match: %q, %v; want %s", id, err, objID)
			}

			c, _, _ = routes(t, body())
			_, err := tt.find(ctx, c, "web")
			var nf *resolve.NotFoundError
			if !errors.As(err, &nf) || nf.Kind != tt.kind || nf.Ref != "web" {
				t.Errorf("no match: error = %v; want a %s NotFoundError", err, tt.kind)
			}

			c, _, _ = routes(t, body(item(objID), item(rsID)))
			_, err = tt.find(ctx, c, "web")
			var amb *resolve.AmbiguousError
			if !errors.As(err, &amb) || amb.Kind != tt.kind || amb.Count != 2 {
				t.Errorf("two matches: error = %v; want a %s AmbiguousError of 2", err, tt.kind)
			}

			c, _, _ = routes(t, map[string]string{tt.path: "500"})
			_, err = tt.find(ctx, c, "web")
			if want := `looking up ` + tt.kind + ` "web": `; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("failed listing: error = %v; want prefix %q", err, want)
			}
		})
	}
}

// FindRecordSets keeps only exact names: designate's name filter also
// matches by wildcard.
func TestFindRecordSetsExact(t *testing.T) {
	c, _, _ := routes(t, map[string]string{
		"/v2/zones/" + zoneID + "/recordsets?name=www.example.com.": `{"recordsets":[` +
			`{"id":"` + rsID + `","name":"www.example.com.","type":"A"},` +
			`{"id":"` + objID + `","name":"www.example.com.","type":"AAAA"},` +
			`{"id":"` + zoneID + `","name":"a.www.example.com.","type":"A"}],"links":{}}`,
	})
	all, err := dns.FindRecordSets(context.Background(), c, zoneID, "www.example.com")
	if err != nil || len(all) != 2 || all[0].Type != "A" || all[1].Type != "AAAA" {
		t.Errorf("FindRecordSets = %+v, %v; want the A and AAAA of www", all, err)
	}
}

// listAll follows links.next and stops at the cap without fetching more.
func TestListPaging(t *testing.T) {
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	var paths []string
	fs.Mux.HandleFunc("/v2/pools", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("marker") == "" {
			_, _ = io.WriteString(w, `{"pools":[{"id":"p1"},{"id":"p2"}],"links":{"next":"`+fs.Server.URL+`/v2/pools?marker=p2"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"pools":[{"id":"p3"}],"links":{}}`)
	})
	c := fakeclient.ServiceClient(fs)
	c.ResourceBase = c.Endpoint + "v2/"
	ctx := context.Background()

	all, err := dns.ListPools(ctx, c, "", 0)
	if err != nil || len(all) != 3 || all[2].ID != "p3" {
		t.Errorf("uncapped = %+v, %v; want p1..p3", all, err)
	}
	if want := []string{"/v2/pools", "/v2/pools?marker=p2"}; strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Errorf("requests = %v; want %v", paths, want)
	}

	paths = nil
	all, err = dns.ListPools(ctx, c, "", 2)
	if err != nil || len(all) != 2 || len(paths) != 1 {
		t.Errorf("capped at 2 = %+v, %v after %d requests; want 2 from one page", all, err, len(paths))
	}
}

func TestListZonesLimit(t *testing.T) {
	c, _, _ := routes(t, map[string]string{
		"/v2/zones": `{"zones":[{"id":"z1"},{"id":"z2"},{"id":"z3"}],"links":{}}`,
	})
	all, err := dns.ListZones(context.Background(), c, zones.ListOpts{}, 2)
	if err != nil || len(all) != 2 {
		t.Errorf("ListZones(limit 2) = %+v, %v", all, err)
	}
}

func TestScope(t *testing.T) {
	if h := (dns.Scope{}).Headers(); h != nil {
		t.Errorf("zero Scope headers = %v; want nil", h)
	}
	s := dns.Scope{AllProjects: true, SudoProjectID: prj}
	want := map[string]string{"X-Auth-All-Projects": "true", "X-Auth-Sudo-Project-ID": prj}
	if h := s.Headers(); len(h) != 2 || h["X-Auth-All-Projects"] != "true" || h["X-Auth-Sudo-Project-ID"] != prj {
		t.Errorf("headers = %v; want %v", h, want)
	}

	c, _ := endpoint(t, expect{method: "GET", path: "/v2/limits", header: want}, 200, `{}`)
	c.MoreHeaders = map[string]string{"X-Custom": "kept"}
	if dns.WithScope(c, dns.Scope{}) != c {
		t.Error("WithScope(zero) copied the client")
	}
	scoped := dns.WithScope(c, s)
	if scoped == c || len(c.MoreHeaders) != 1 {
		t.Errorf("WithScope changed the original client: %v", c.MoreHeaders)
	}
	if scoped.MoreHeaders["X-Custom"] != "kept" {
		t.Errorf("WithScope dropped the client's headers: %v", scoped.MoreHeaders)
	}
	if _, err := dns.GetLimits(context.Background(), scoped); err != nil {
		t.Fatal(err)
	}
}

func TestZoneUpdateMap(t *testing.T) {
	m, err := dns.ZoneUpdateMap{"ttl": 0}.ToZoneUpdateMap()
	if err != nil || m["ttl"] != 0 {
		t.Errorf("ToZoneUpdateMap = %v, %v", m, err)
	}
}
