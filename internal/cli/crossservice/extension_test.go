package crossservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// extensionServer serves one service's GET /extensions and counts the calls.
func extensionServer(t *testing.T, typ, body string, calls *int) clientFunc {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		*calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	sc := serviceClient(fakeServer, typ, "")
	return func() (*gophercloud.ServiceClient, error) { return sc, nil }
}

func TestRunExtensionList_MergesServicesAndHonoursFlags(t *testing.T) {
	var nova, cinder, neutron int
	svc := extensionServices{
		compute: extensionServer(t, "compute", `{"extensions": [{"name": "Aggregates", "alias": "os-aggregates",
		  "description": "Admin-only aggregate administration.", "namespace": "http://docs.openstack.org/compute/ext/fake_xml",
		  "updated": "2014-12-03T00:00:00Z", "links": []}]}`, &nova),
		volume:  extensionServer(t, "volume", `{"extensions": [{"name": "Backups", "alias": "backups", "description": "Backups support."}]}`, &cinder),
		network: extensionServer(t, "network", `{"extensions": [{"name": "Neutron L3 Router", "alias": "router", "description": "Router abstraction"}]}`, &neutron),
	}

	o := &output.Options{Format: output.FormatJSON}
	var out, errw bytes.Buffer
	if err := runExtensionList(context.Background(), svc, o, &extensionListFlags{long: true}, &out, &errw); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0]["Alias"] != "os-aggregates" || rows[1]["Alias"] != "backups" || rows[2]["Alias"] != "router" {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["Namespace"] != "http://docs.openstack.org/compute/ext/fake_xml" || rows[0]["Updated At"] != "2014-12-03T00:00:00Z" {
		t.Errorf("--long columns = %v", rows[0])
	}
	if errw.Len() != 0 {
		t.Errorf("unexpected warnings: %s", errw.String())
	}

	// A service flag narrows the listing to that service.
	out.Reset()
	if err := runExtensionList(context.Background(), svc, o, &extensionListFlags{network: true}, &out, &errw); err != nil {
		t.Fatal(err)
	}
	if nova != 1 || cinder != 1 || neutron != 2 {
		t.Errorf("calls nova=%d cinder=%d neutron=%d, want 1 1 2", nova, cinder, neutron)
	}
	if !strings.Contains(out.String(), `"Alias": "router"`) || strings.Contains(out.String(), "Namespace") {
		t.Errorf("--network output = %s", out.String())
	}
}

// As upstream, a service that cannot answer is a warning and the rest still
// list; keystone v3 has no extensions, so --identity only warns.
func TestRunExtensionList_UnavailableServiceWarns(t *testing.T) {
	var neutron int
	svc := extensionServices{
		compute: func() (*gophercloud.ServiceClient, error) { return nil, errors.New("no compute endpoint") },
		volume:  func() (*gophercloud.ServiceClient, error) { return nil, errors.New("no volume endpoint") },
		network: extensionServer(t, "network", `{"extensions": [{"name": "Trunk", "alias": "trunk"}]}`, &neutron),
	}
	o := &output.Options{Format: output.FormatValue}
	var out, errw bytes.Buffer
	if err := runExtensionList(context.Background(), svc, o, &extensionListFlags{identity: true, compute: true, network: true}, &out, &errw); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Trunk trunk \n" {
		t.Errorf("output = %q", out.String())
	}
	for _, want := range []string{"not supported by Identity API", "not supported by Compute API: no compute endpoint"} {
		if !strings.Contains(errw.String(), want) {
			t.Errorf("warnings %q lack %q", errw.String(), want)
		}
	}
	if strings.Contains(errw.String(), "Block Storage") {
		t.Errorf("volume was queried without --volume: %s", errw.String())
	}
}

func TestRunExtensionShow_ByAliasThenName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions/router", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extension": {"name": "Neutron L3 Router", "alias": "router", "description": "Router abstraction", "updated": "2012-07-20T10:00:00-00:00"}}`))
	})
	// Any other ref is not an alias: neutron answers 404.
	fakeServer.Mux.HandleFunc("/extensions/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extensions": [{"name": "Trunk Extension", "alias": "trunk", "description": "Provides support for trunk ports"}]}`))
	})
	sc := serviceClient(fakeServer, "network", "")

	for ref, want := range map[string]string{
		"router":          "router Router abstraction Neutron L3 Router 2012-07-20T10:00:00-00:00\n",
		"Trunk Extension": "trunk Provides support for trunk ports Trunk Extension \n",
	} {
		o := &output.Options{Format: output.FormatValue}
		var out bytes.Buffer
		if err := runExtensionShow(context.Background(), sc, o, ref, &out); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := strings.ReplaceAll(out.String(), "\n", " "); strings.TrimSpace(got) != strings.TrimSpace(strings.ReplaceAll(want, "\n", " ")) {
			t.Errorf("%s: output = %q, want %q", ref, out.String(), want)
		}
	}
	if err := runExtensionShow(context.Background(), sc, &output.Options{Format: output.FormatValue}, "nope", &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), `no extension found for "nope"`) {
		t.Errorf("err = %v", err)
	}
}
