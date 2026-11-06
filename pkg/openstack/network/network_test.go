package network_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack"
	"github.com/ftarasenko/koc/pkg/openstack/network"
)

const projectID = "2f1e0d9c-8b7a-4c6d-9e5f-4a3b2c1d0e9f"

type call struct{ Method, Path, Body string }

// neutron is a fake network endpoint that answers "METHOD /path" from routes,
// 404 otherwise, and records every request with its compact body.
func neutron(t *testing.T, routes map[string]string) (*[]call, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	var seen []call
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			body = string(b)
		}
		seen = append(seen, call{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		out, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"NeutronError":{"type":"NotFound","message":"not found"}}`)
			return
		}
		_, _ = io.WriteString(w, out)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "network"
	return &seen, sc
}

func TestQuotas(t *testing.T) {
	seen, sc := neutron(t, map[string]string{
		"GET /quotas/" + projectID:              `{"quota":{"network":10,"port":50}}`,
		"GET /quotas/" + projectID + "/default": `{"quota":{"network":100}}`,
		"PUT /quotas/" + projectID:              `{"quota":{"network":5,"port":50}}`,
		"GET /quotas/empty":                     `{}`,
	})
	ctx := context.Background()

	if q, err := network.GetQuota(ctx, sc, projectID); err != nil || q.Network != 10 || q.Port != 50 {
		t.Fatalf("GetQuota = %+v, %v", q, err)
	}
	if q, err := network.GetDefaultQuota(ctx, sc, projectID); err != nil || q.Network != 100 {
		t.Fatalf("GetDefaultQuota = %+v, %v", q, err)
	}
	n := 5
	if q, err := network.UpdateQuota(ctx, sc, projectID, quotas.UpdateOpts{Network: &n}); err != nil || q.Network != 5 {
		t.Fatalf("UpdateQuota = %+v, %v", q, err)
	}
	wantCalls := []call{
		{"GET", "/quotas/" + projectID, ""},
		{"GET", "/quotas/" + projectID + "/default", ""},
		{"PUT", "/quotas/" + projectID, `{"quota":{"network":5}}`},
	}
	if len(*seen) != len(wantCalls) {
		t.Fatalf("requests = %+v", *seen)
	}
	for i, w := range wantCalls {
		if (*seen)[i] != w {
			t.Errorf("request %d = %+v; want %+v", i, (*seen)[i], w)
		}
	}

	_, err := network.GetQuota(ctx, sc, "empty")
	if !errors.Is(err, openstack.ErrNoObject) ||
		err.Error() != `showing network quotas for project "empty": the server returned success but no object` {
		t.Errorf("empty reply: %v", err)
	}
	for _, tc := range []struct {
		err  error
		text string
	}{
		{second(network.GetQuota(ctx, sc, "nope")), `showing network quotas for project "nope": `},
		{second(network.GetDefaultQuota(ctx, sc, "nope")), `showing default network quotas for project "nope": `},
		{second(network.UpdateQuota(ctx, sc, "nope", quotas.UpdateOpts{Network: &n})), `setting network quotas for project "nope": `},
		{second(network.ListAvailabilityZones(ctx, sc)), `listing network availability zones: `},
	} {
		if !gophercloud.ResponseCodeIs(tc.err, http.StatusNotFound) || !strings.HasPrefix(tc.err.Error(), tc.text) {
			t.Errorf("error = %v; want %q and the 404", tc.err, tc.text)
		}
	}
}

func TestListAvailabilityZones(t *testing.T) {
	_, sc := neutron(t, map[string]string{
		"GET /availability_zones": `{"availability_zones":[
			{"name":"nova","resource":"network","state":"available"},
			{"name":"nova","resource":"router","state":"unavailable"}]}`,
	})
	got, err := network.ListAvailabilityZones(context.Background(), sc)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListAvailabilityZones = %+v, %v", got, err)
	}
	if want := (network.AvailabilityZone{Name: "nova", Resource: "router", State: "unavailable"}); got[1] != want {
		t.Errorf("zone = %+v; want %+v", got[1], want)
	}
}

func second[T any](_ T, err error) error { return err }
