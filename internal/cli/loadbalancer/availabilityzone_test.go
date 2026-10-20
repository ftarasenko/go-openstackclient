package loadbalancer

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const azpBody = `{"id": "azp1", "name": "edge", "provider_name": "amphora", "availability_zone_data": "{\"compute_zone\": \"az1\"}"}`

func TestRunAZProfileCreate_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzoneprofiles", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"availability_zone_profile": {"name": "edge", "provider_name": "amphora",
			"availability_zone_data": "{\"compute_zone\": \"az1\"}"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"availability_zone_profile": ` + azpBody + `}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"id", "availability_zone_data"}}
	var buf bytes.Buffer
	if err := runAZProfileCreate(context.Background(), lbClient(fakeServer), o, "edge", "amphora", `{"compute_zone": "az1"}`, &buf); err != nil {
		t.Fatalf("runAZProfileCreate: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "azp1") || !strings.Contains(got, "compute_zone") {
		t.Errorf("output = %q", got)
	}
}

// The listing follows octavia's next link, whose key is the singular
// "availability_zone_profile_links".
func TestRunAZProfileList_FiltersAndPaging(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzoneprofiles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("marker") == "" {
			th.TestFormValues(t, r, map[string]string{"provider_name": "amphora"})
			_, _ = w.Write([]byte(`{"availability_zone_profiles": [` + azpBody + `],
				"availability_zone_profile_links": [{"rel": "next", "href": "` + fakeServer.Endpoint() +
				`v2.0/lbaas/availabilityzoneprofiles?provider_name=amphora&marker=azp1"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"availability_zone_profiles": [{"id": "azp2", "name": "core", "provider_name": "amphora"}],
			"availability_zone_profile_links": []}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"ID"}}
	var buf bytes.Buffer
	if err := runAZProfileList(context.Background(), lbClient(fakeServer), o, "", "amphora", &buf); err != nil {
		t.Fatalf("runAZProfileList: %v", err)
	}
	if got := strings.Fields(buf.String()); strings.Join(got, ",") != "azp1,azp2" {
		t.Errorf("ids = %v, want both pages", got)
	}
}

// create resolves the profile by name; --disable sends an explicit false.
func TestRunAZCreate_ResolvesProfileAndDisables(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzoneprofiles", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "edge"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"availability_zone_profiles": [` + azpBody + `]}`))
	})
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzones", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"availability_zone": {"name": "az-edge", "availability_zone_profile_id": "azp1",
			"description": "edge racks", "enabled": false}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"availability_zone": {"name": "az-edge", "availability_zone_profile_id": "azp1",
			"description": "edge racks", "enabled": false}}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"enabled"}}
	var buf bytes.Buffer
	f := &azCreateFlags{profile: "edge", description: "edge racks", disable: true}
	if err := runAZCreate(context.Background(), lbClient(fakeServer), o, "az-edge", f, &buf); err != nil {
		t.Fatalf("runAZCreate: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "false" {
		t.Errorf("enabled = %q", buf.String())
	}
}

// An availability zone is addressed by name, escaped into the path.
func TestRunAZSetShowDelete_ByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var methods []string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzones/", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if got := r.URL.EscapedPath(); got != "/v2.0/lbaas/availabilityzones/rack%201" {
			t.Errorf("path = %q, want the name escaped", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"availability_zone": {"enabled": true}}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"availability_zone": {"name": "rack 1", "enabled": true}}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"name"}}
	ctx, client := context.Background(), lbClient(fakeServer)
	var buf bytes.Buffer
	if err := runAZSet(ctx, client, o, "rack 1", map[string]any{"enabled": true}, &buf); err != nil {
		t.Fatalf("runAZSet: %v", err)
	}
	if err := runAZShow(ctx, client, o, "rack 1", &buf); err != nil {
		t.Fatalf("runAZShow: %v", err)
	}
	if err := runAZDelete(ctx, client, []string{"rack 1"}, &buf); err != nil {
		t.Fatalf("runAZDelete: %v", err)
	}
	if strings.Join(methods, ",") != "PUT,GET,DELETE" {
		t.Errorf("requests = %v", methods)
	}
	if !strings.Contains(buf.String(), "Deleted availability zone rack 1") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunAZList_Filters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzones", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "az1", "availability_zone_profile_id": "8ef2a3f0-0000-4000-8000-000000000001", "enabled": "false"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"availability_zones": [{"name": "az1", "availability_zone_profile_id": "8ef2a3f0-0000-4000-8000-000000000001", "enabled": false}]}`))
	})

	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	f := &azListFlags{name: "az1", profile: "8ef2a3f0-0000-4000-8000-000000000001", disable: true}
	if err := runAZList(context.Background(), lbClient(fakeServer), o, f, &buf); err != nil {
		t.Fatalf("runAZList: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "az1 ") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunAZProfileSetAndDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "8ef2a3f0-0000-4000-8000-000000000002"
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzoneprofiles/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"availability_zone_profile": {"availability_zone_data": "{}"}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"availability_zone_profile": ` + azpBody + `}`))
	})

	o := &output.Options{Format: output.FormatTable}
	ctx, client := context.Background(), lbClient(fakeServer)
	var buf bytes.Buffer
	if err := runAZProfileSet(ctx, client, o, id, map[string]any{"availability_zone_data": "{}"}, &buf); err != nil {
		t.Fatalf("runAZProfileSet: %v", err)
	}
	if err := runAZProfileDelete(ctx, client, []string{id}, &buf); err != nil {
		t.Fatalf("runAZProfileDelete: %v", err)
	}
}

func TestRunUnset_AvailabilityZoneDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/availabilityzones/az1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"availability_zone": {"description": null}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"availability_zone": {"name": "az1", "enabled": true}}`))
	})
	o := &output.Options{Format: output.FormatTable}
	err := runUnset(context.Background(), lbClient(fakeServer), o, azUnsetSpec,
		unsetTarget{path: []string{"lbaas", azCollection, "az1"}, ref: "az1"}, unsetRequest{attrs: []string{"description"}}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runUnset: %v", err)
	}
}
