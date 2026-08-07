package dns

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunDNSPoolList_AndShowByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/pools", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "pools": [{"id": "p1", "name": "default", "description": "the only pool",
                     "ns_records": [{"hostname": "ns1.example.com.", "priority": 1},
                                    {"hostname": "ns2.example.com.", "priority": 2}]}],
          "links": {}
        }`))
	})
	fakeServer.Mux.HandleFunc("/pools/p1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "p1", "name": "default", "project_id": "admin",
          "ns_records": [{"hostname": "ns1.example.com.", "priority": 1}]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	client := dnsShareClient(fakeServer)
	var buf bytes.Buffer
	if err := runDNSPoolList(context.Background(), client, o, "", 0, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runDNSPoolList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	// Nameservers render as priority:hostname, one per line.
	for _, want := range []string{"default", "1:ns1.example.com.", "2:ns2.example.com."} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
	// A pool name resolves to its ID, so "show default" must hit /pools/p1.
	buf.Reset()
	if err := runDNSPoolShow(context.Background(), client, o, "default", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runDNSPoolShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "p1") {
		t.Errorf("output missing the pool id\n---\n%s", buf.String())
	}
}

func TestRunPTRRecordList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/reverse/floatingips", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "floatingips": [{"id": "RegionOne:f1", "ptrdname": "host.example.com.",
                           "description": "web", "ttl": 300, "address": "10.0.0.5"}],
          "links": {}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPTRRecordList(context.Background(), dnsShareClient(fakeServer), o, 0,
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runPTRRecordList error: %v", err)
	}
	for _, want := range []string{"RegionOne:f1", "host.example.com.", "300"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// designate keys a reverse record by "<region>:<floating-ip-id>". A bare UUID gets
// a bare 404 from the API, so reject it locally with a message that says why.
func TestValidatePTRRecordID(t *testing.T) {
	if err := validatePTRRecordID("RegionOne:f1"); err != nil {
		t.Errorf("valid id rejected: %v", err)
	}
	for _, bad := range []string{"8f2b3d5a-1e6c-4a7b-9c0d-2e3f4a5b6c7d", ":f1", "nocolon"} {
		if err := validatePTRRecordID(bad); err == nil {
			t.Errorf("id %q accepted, want an error", bad)
		}
	}
}

func TestRunPTRRecordSet_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/reverse/floatingips/RegionOne:f1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "Content-Type", "application/json")
		// ptrdname is always sent: a nil one is how the record is removed.
		th.TestJSONRequest(t, r, `{"ptrdname": "host.example.com.", "ttl": 300}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "RegionOne:f1", "ptrdname": "host.example.com.",
          "ttl": 300, "status": "PENDING", "action": "CREATE"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &ptrRecordSetFlags{ttl: 300}
	if err := runPTRRecordSet(context.Background(), dnsShareClient(fakeServer), o,
		"RegionOne:f1", "host.example.com.", f, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runPTRRecordSet error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if !strings.Contains(buf.String(), "PENDING") {
		t.Errorf("output missing the status\n---\n%s", buf.String())
	}
}

func TestRunPTRRecordSet_NoTTLSendsNull(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/reverse/floatingips/RegionOne:f1", func(w http.ResponseWriter, r *http.Request) {
		// Clearing the TTL needs an explicit null; omitting the key would keep it.
		th.TestJSONRequest(t, r, `{"ptrdname": "host.example.com.", "ttl": null}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "RegionOne:f1", "ptrdname": "host.example.com."}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &ptrRecordSetFlags{noTTL: true}
	if err := runPTRRecordSet(context.Background(), dnsShareClient(fakeServer), o,
		"RegionOne:f1", "host.example.com.", f, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runPTRRecordSet error: %v", err)
	}
}

// Unsetting is a PATCH with a null ptrdname — designate has no DELETE here, because
// the floating IP itself is not going away.
func TestRunPTRRecordUnset_PatchesNullPTRDName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/reverse/floatingips/RegionOne:f1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"ptrdname": null}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "RegionOne:f1", "ptrdname": null}`))
	})

	var buf bytes.Buffer
	if err := runPTRRecordUnset(context.Background(), dnsShareClient(fakeServer),
		"RegionOne:f1", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runPTRRecordUnset error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if !strings.Contains(buf.String(), "Unset PTR record for floating IP RegionOne:f1") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunDNSServiceList_FiltersAndFormatsMultiValues(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/service_statuses", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "service_statuses": [
            {"id": "s1", "hostname": "dns-1", "service_name": "central", "status": "UP",
             "stats": {}, "capabilities": {}},
            {"id": "s2", "hostname": "dns-2", "service_name": "worker", "status": "UP",
             "stats": {"zones": 42, "rrsets": 108}, "capabilities": {"pools": "default"}}
          ],
          "links": {}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &dnsServiceListFlags{hostname: "dns-1", serviceName: "central", status: "UP"}
	if err := runDNSServiceList(context.Background(), dnsShareClient(fakeServer), o, f,
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runDNSServiceList error: %v", err)
	}
	// The API spells the filter service_name even though the flag is --service-name.
	for _, want := range []string{"hostname=dns-1", "service_name=central", "status=UP"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query = %q, want %q", gotQuery, want)
		}
	}
	// Empty stats/capabilities objects render as "-", matching upstream; a
	// populated one renders one sorted "key=value" per line.
	out := buf.String()
	for _, want := range []string{"central", "-", "rrsets=108", "zones=42", "pools=default"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	// Sorted by key: rrsets before zones.
	if i, j := strings.Index(out, "rrsets=108"), strings.Index(out, "zones=42"); i > j {
		t.Errorf("stats keys are not sorted\n---\n%s", out)
	}
}

// Upstream spells the filter --service_name; keep that spelling working while
// showing only the dashed one in --help.
func TestDNSServiceList_UnderscoreFlagAlias(t *testing.T) {
	cmd := newDNSServiceListCommand(&auth.Options{}, &output.Options{Format: output.FormatTable})
	alias := cmd.Flags().Lookup("service_name")
	if alias == nil {
		t.Fatal("--service_name is not registered")
	}
	if !alias.Hidden {
		t.Error("--service_name should be hidden so the dashed form is the documented one")
	}
	if cmd.Flags().Lookup("service-name") == nil {
		t.Error("--service-name is not registered")
	}
}

func TestRunDNSServiceShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/service_statuses/s1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		// The real designate payload: stats and capabilities are objects, and are
		// empty on a healthy central. Rendering them must not fail.
		_, _ = w.Write([]byte(`{"id": "s1", "hostname": "dns-1", "service_name": "central",
          "status": "UP", "stats": {}, "capabilities": {},
          "heartbeated_at": "2026-08-06T12:00:00.000000"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runDNSServiceShow(context.Background(), dnsShareClient(fakeServer), o, "s1",
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runDNSServiceShow error: %v", err)
	}
	for _, want := range []string{"dns-1", "central", "UP"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// A malformed floating-IP reference must be refused before any request goes out.
func TestPTRRecordCommands_RejectBareUUID(t *testing.T) {
	a := &auth.Options{}
	o := &output.Options{Format: output.FormatTable}
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"show", newPTRRecordShowCommand(a, o), []string{"f1"}},
		{"set", newPTRRecordSetCommand(a, o), []string{"f1", "host.example.com."}},
		{"unset", newPTRRecordUnsetCommand(a, o), []string{"f1"}},
	} {
		tc.cmd.SetOut(&bytes.Buffer{})
		tc.cmd.SetErr(&bytes.Buffer{})
		tc.cmd.SetContext(context.Background())
		tc.cmd.SetArgs(tc.args)
		err := tc.cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "<region>:<floating-ip-id>") {
			t.Errorf("%s: error = %v, want the region-prefix error", tc.name, err)
		}
	}
}
