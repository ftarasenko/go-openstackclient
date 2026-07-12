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

func TestRunZoneCreate_SecondaryBodyHasMastersNoEmail(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
          "id": "44444444-4444-4444-4444-444444444444",
          "name": "secondary.example.",
          "type": "SECONDARY",
          "masters": ["192.0.2.53"],
          "status": "PENDING",
          "action": "CREATE"
        }`))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &zoneCreateFlags{typ: "SECONDARY", masters: []string{"192.0.2.53"}}

	var buf bytes.Buffer
	if err := runZoneCreate(context.Background(), client, o, "secondary.example", f, &buf); err != nil {
		t.Fatalf("runZoneCreate returned error: %v", err)
	}

	if gotBody["type"] != "SECONDARY" {
		t.Errorf("body type = %v, want SECONDARY", gotBody["type"])
	}
	masters, ok := gotBody["masters"].([]any)
	if !ok || len(masters) != 1 || masters[0] != "192.0.2.53" {
		t.Errorf("body masters = %v, want [192.0.2.53]", gotBody["masters"])
	}
	if _, present := gotBody["email"]; present {
		t.Errorf("body must not contain email for SECONDARY zones, got %v", gotBody["email"])
	}
}

func TestRunZoneCreate_PrimaryRequiresEmail(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// The request must never reach the server: validation fails first.
	fakeServer.Mux.HandleFunc("/zones", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("runZoneCreate should not issue a request for an invalid PRIMARY zone")
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &zoneCreateFlags{typ: "PRIMARY"} // no email

	var buf bytes.Buffer
	err := runZoneCreate(context.Background(), client, o, "example.org", f, &buf)
	if err == nil {
		t.Fatal("runZoneCreate should fail when PRIMARY zone has no --email")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Errorf("error = %q, want it to mention email", err.Error())
	}
}

func TestRunZoneList_LimitTruncates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneListBody))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runZoneList(context.Background(), client, o, &zoneListFlags{limit: 1}, &buf); err != nil {
		t.Fatalf("runZoneList returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "example.com.") {
		t.Errorf("output missing first (kept) zone:\n%s", out)
	}
	if strings.Contains(out, "example.net.") {
		t.Errorf("--limit 1 should truncate the second zone:\n%s", out)
	}
}

func TestRunZoneSet_TTLZeroIsSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveZoneID lists zones to map the reference to an ID.
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneListBody))
	})

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/zones/11111111-1111-1111-1111-111111111111", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
          "id": "11111111-1111-1111-1111-111111111111",
          "name": "example.com.",
          "type": "PRIMARY",
          "ttl": 0,
          "status": "PENDING",
          "action": "UPDATE"
        }`))
	})

	client := dnsClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &zoneSetFlags{ttl: 0}

	var buf bytes.Buffer
	// emailSet=false, ttlSet=true, descSet=false
	if err := runZoneSet(context.Background(), client, o, "11111111-1111-1111-1111-111111111111", f, false, true, false, &buf); err != nil {
		t.Fatalf("runZoneSet returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	ttl, present := gotBody["ttl"]
	if !present {
		t.Fatalf("body must contain ttl when --ttl is changed, got %v", gotBody)
	}
	if v, ok := ttl.(float64); !ok || int(v) != 0 {
		t.Errorf("body ttl = %v, want 0", gotBody["ttl"])
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
