package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// dnsClient returns a service client wired to the mock server with the designate
// service type. The DNS client uses no microversion header.
func dnsClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "dns"
	return sc
}

const zoneListBody = `{
  "zones": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "example.com.",
      "type": "PRIMARY",
      "email": "admin@example.com",
      "ttl": 3600,
      "serial": 1500000000,
      "status": "ACTIVE",
      "action": "NONE"
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "name": "example.net.",
      "type": "PRIMARY",
      "email": "admin@example.net",
      "ttl": 7200,
      "serial": 1500000001,
      "status": "ACTIVE",
      "action": "NONE"
    }
  ]
}`

func TestRunZoneList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneListBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runZoneList(context.Background(), client, o, &zoneListFlags{}, &buf); err != nil {
		t.Fatalf("runZoneList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Type", "Email", "TTL", "Serial", "Status",
		"example.com.", "example.net.", "PRIMARY", "admin@example.com",
		"11111111-1111-1111-1111-111111111111",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunZoneCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
          "id": "33333333-3333-3333-3333-333333333333",
          "name": "example.org.",
          "type": "PRIMARY",
          "email": "test@example.org",
          "ttl": 3600,
          "status": "PENDING",
          "action": "CREATE"
        }`))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &zoneCreateFlags{email: "test@example.org", ttl: 3600, typ: "PRIMARY"}

	var buf bytes.Buffer
	if err := runZoneCreate(context.Background(), client, o, "example.org", f, &buf); err != nil {
		t.Fatalf("runZoneCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotBody["name"] != "example.org." {
		t.Errorf("body name = %v, want example.org. (trailing dot added)", gotBody["name"])
	}
	if gotBody["email"] != "test@example.org" {
		t.Errorf("body email = %v, want test@example.org", gotBody["email"])
	}
	if gotBody["type"] != "PRIMARY" {
		t.Errorf("body type = %v, want PRIMARY", gotBody["type"])
	}
	if ttl, ok := gotBody["ttl"].(float64); !ok || int(ttl) != 3600 {
		t.Errorf("body ttl = %v, want 3600", gotBody["ttl"])
	}
	if !strings.Contains(buf.String(), "33333333-3333-3333-3333-333333333333") {
		t.Errorf("output missing created zone id:\n%s", buf.String())
	}
}

const recordSetListBody = `{
  "recordsets": [
    {
      "id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
      "zone_id": "11111111-1111-1111-1111-111111111111",
      "name": "www.example.com.",
      "type": "A",
      "records": ["192.0.2.1"],
      "ttl": 3600,
      "status": "ACTIVE",
      "action": "NONE"
    }
  ]
}`

func TestRunRecordSetList_ResolvesZoneAndLists(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// Zone name -> ID resolution lists all zones.
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneListBody))
	})

	var gotMethod, gotURL string
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111/recordsets", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(recordSetListBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	// Reference the zone by name (without trailing dot) to exercise resolution.
	if err := runRecordSetList(context.Background(), client, o, "example.com", &recordSetListFlags{}, &buf); err != nil {
		t.Fatalf("runRecordSetList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotURL != "/zones/11111111-1111-1111-1111-111111111111/recordsets" {
		t.Errorf("recordset list URL = %q, want nested under resolved zone id", gotURL)
	}
	out := buf.String()
	for _, want := range []string{"www.example.com.", "192.0.2.1", "A", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
		if !strings.Contains(out, want) {
			t.Errorf("recordset table output missing %q\n---\n%s", want, out)
		}
	}
}

// zoneShowBody is the single-zone GET response for the first zone in
// zoneListBody, used by the show/set flows.
const zoneShowBody = `{
  "id": "11111111-1111-1111-1111-111111111111",
  "name": "example.com.",
  "type": "PRIMARY",
  "email": "admin@example.com",
  "ttl": 3600,
  "serial": 1500000000,
  "status": "ACTIVE",
  "action": "NONE",
  "description": "primary zone"
}`

// registerZoneList wires the /zones list endpoint used for name->ID resolution.
func registerZoneList(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneListBody))
	})
}

func TestRunZoneShow_ResolvesByNameAndGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)

	var gotMethod, gotURL string
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneShowBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runZoneShow(context.Background(), client, o, "example.com", &buf); err != nil {
		t.Fatalf("runZoneShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotURL != "/zones/11111111-1111-1111-1111-111111111111" {
		t.Errorf("show URL = %q, want /zones/<resolved id>", gotURL)
	}
	out := buf.String()
	for _, want := range []string{"Field", "Value", "11111111-1111-1111-1111-111111111111", "example.com.", "admin@example.com", "primary zone"} {
		if !strings.Contains(out, want) {
			t.Errorf("zone show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunZoneDelete_ResolvesAndDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)

	var gotMethod, gotURL string
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(zoneShowBody))
	})

	client := dnsClient(fakeServer)

	var buf bytes.Buffer
	if err := runZoneDelete(context.Background(), client, []string{"example.com"}, &buf); err != nil {
		t.Fatalf("runZoneDelete returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotURL != "/zones/11111111-1111-1111-1111-111111111111" {
		t.Errorf("delete URL = %q, want /zones/<resolved id>", gotURL)
	}
	if !strings.Contains(buf.String(), "Deleted zone example.com") {
		t.Errorf("delete output missing confirmation:\n%s", buf.String())
	}
}

func TestRunZoneSet_PatchesBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneShowBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &zoneSetFlags{email: "new@example.com", ttl: 7200, description: "updated"}

	var buf bytes.Buffer
	if err := runZoneSet(context.Background(), client, o, "example.com", f, true, &buf); err != nil {
		t.Fatalf("runZoneSet returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	if gotBody["email"] != "new@example.com" {
		t.Errorf("body email = %v, want new@example.com", gotBody["email"])
	}
	if ttl, ok := gotBody["ttl"].(float64); !ok || int(ttl) != 7200 {
		t.Errorf("body ttl = %v, want 7200", gotBody["ttl"])
	}
	if gotBody["description"] != "updated" {
		t.Errorf("body description = %v, want updated", gotBody["description"])
	}
	if !strings.Contains(buf.String(), "11111111-1111-1111-1111-111111111111") {
		t.Errorf("zone set output missing zone id:\n%s", buf.String())
	}
}

func TestRunZoneSet_NoFieldsErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	err := runZoneSet(context.Background(), client, o, "example.com", &zoneSetFlags{}, false, &buf)
	if err == nil {
		t.Fatal("runZoneSet with no fields should error")
	}
	if !strings.Contains(err.Error(), "at least one of") {
		t.Errorf("error = %v, want mention of required fields", err)
	}
}

// recordSetShowBody is the single-recordset GET response for the recordset in
// recordSetListBody, used by the show/set flows.
const recordSetShowBody = `{
  "id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
  "zone_id": "11111111-1111-1111-1111-111111111111",
  "zone_name": "example.com.",
  "name": "www.example.com.",
  "type": "A",
  "records": ["192.0.2.1"],
  "ttl": 3600,
  "status": "ACTIVE",
  "action": "NONE",
  "description": "web host"
}`

// registerRecordSetList wires the recordset list endpoint (nested under the
// resolved zone id) used for recordset name->ID resolution.
func registerRecordSetList(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111/recordsets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			// Non-GET (e.g. POST create) falls through to a dedicated handler.
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(recordSetListBody))
	})
}

func TestRunRecordSetShow_ResolvesZoneAndRecordSet(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)
	registerRecordSetList(fakeServer)

	var gotMethod, gotURL string
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111/recordsets/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(recordSetShowBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runRecordSetShow(context.Background(), client, o, "example.com", "www.example.com", &buf); err != nil {
		t.Fatalf("runRecordSetShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotURL != "/zones/11111111-1111-1111-1111-111111111111/recordsets/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("show URL = %q, want nested under resolved zone/recordset id", gotURL)
	}
	out := buf.String()
	for _, want := range []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "www.example.com.", "192.0.2.1", "web host"} {
		if !strings.Contains(out, want) {
			t.Errorf("recordset show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunRecordSetCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111/recordsets", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(recordSetShowBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &recordSetCreateFlags{typ: "A", records: []string{"192.0.2.1"}, ttl: 3600, description: "web host"}

	var buf bytes.Buffer
	if err := runRecordSetCreate(context.Background(), client, o, "example.com", "www", f, &buf); err != nil {
		t.Fatalf("runRecordSetCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotBody["name"] != "www." {
		t.Errorf("body name = %v, want www. (trailing dot added)", gotBody["name"])
	}
	if gotBody["type"] != "A" {
		t.Errorf("body type = %v, want A", gotBody["type"])
	}
	records, ok := gotBody["records"].([]any)
	if !ok || len(records) != 1 || records[0] != "192.0.2.1" {
		t.Errorf("body records = %v, want [192.0.2.1]", gotBody["records"])
	}
	if ttl, ok := gotBody["ttl"].(float64); !ok || int(ttl) != 3600 {
		t.Errorf("body ttl = %v, want 3600", gotBody["ttl"])
	}
	if !strings.Contains(buf.String(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa") {
		t.Errorf("output missing created recordset id:\n%s", buf.String())
	}
}

func TestRunRecordSetDelete_ResolvesAndDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)
	registerRecordSetList(fakeServer)

	var gotMethod, gotURL string
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111/recordsets/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	})

	client := dnsClient(fakeServer)

	var buf bytes.Buffer
	if err := runRecordSetDelete(context.Background(), client, "example.com", []string{"www.example.com"}, &buf); err != nil {
		t.Fatalf("runRecordSetDelete returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotURL != "/zones/11111111-1111-1111-1111-111111111111/recordsets/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("delete URL = %q, want nested under resolved ids", gotURL)
	}
	if !strings.Contains(buf.String(), "Deleted recordset www.example.com") {
		t.Errorf("delete output missing confirmation:\n%s", buf.String())
	}
}

func TestRunRecordSetSet_PutBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	registerZoneList(fakeServer)
	registerRecordSetList(fakeServer)

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111/recordsets/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(recordSetShowBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &recordSetSetFlags{records: []string{"198.51.100.5"}, ttl: 600, description: "moved"}

	var buf bytes.Buffer
	if err := runRecordSetSet(context.Background(), client, o, "example.com", "www.example.com", f, true, true, true, &buf); err != nil {
		t.Fatalf("runRecordSetSet returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	records, ok := gotBody["records"].([]any)
	if !ok || len(records) != 1 || records[0] != "198.51.100.5" {
		t.Errorf("body records = %v, want [198.51.100.5]", gotBody["records"])
	}
	if ttl, ok := gotBody["ttl"].(float64); !ok || int(ttl) != 600 {
		t.Errorf("body ttl = %v, want 600", gotBody["ttl"])
	}
	if gotBody["description"] != "moved" {
		t.Errorf("body description = %v, want moved", gotBody["description"])
	}
	if !strings.Contains(buf.String(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa") {
		t.Errorf("recordset set output missing recordset id:\n%s", buf.String())
	}
}

func TestRunRecordSetSet_NoFieldsErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	err := runRecordSetSet(context.Background(), client, o, "example.com", "www.example.com", &recordSetSetFlags{}, false, false, false, &buf)
	if err == nil {
		t.Fatal("runRecordSetSet with no fields should error")
	}
	if !strings.Contains(err.Error(), "at least one of") {
		t.Errorf("error = %v, want mention of required fields", err)
	}
}
