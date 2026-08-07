package dns

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunZoneExportCreate_PostsToZoneTask(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	var gotMethod string
	fakeServer.Mux.HandleFunc("/zones/z1/tasks/export", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{
          "id": "e1", "zone_id": "z1", "project_id": "p1", "status": "PENDING",
          "location": "designate://v2/zones/tasks/exports/e1/export", "version": 1,
          "created_at": "2026-08-06T12:00:00.000000"
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// The zone is given by name, so resolveZoneID turns it into z1 first.
	if err := runZoneExportCreate(context.Background(), dnsShareClient(fakeServer), o,
		"example.com", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneExportCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	for _, want := range []string{"e1", "z1", "PENDING"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunZoneExportList_FiltersAndPaginates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQueries []string
	fakeServer.Mux.HandleFunc("/zones/tasks/exports", func(w http.ResponseWriter, r *http.Request) {
		gotQueries = append(gotQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		// Designate paginates with an absolute links.next URL; the first response
		// carries one so the walk has to follow it exactly once.
		if r.URL.Query().Get("marker") == "" {
			_, _ = w.Write([]byte(`{
              "exports": [{"id": "e1", "zone_id": "z1", "status": "COMPLETE",
                           "created_at": "2026-08-06T12:00:00.000000"}],
              "links": {"next": "` + fakeServer.Server.URL + `/zones/tasks/exports?marker=e1&status=COMPLETE"}
            }`))
			return
		}
		_, _ = w.Write([]byte(`{
          "exports": [{"id": "e2", "zone_id": "z2", "status": "COMPLETE",
                       "created_at": "2026-08-06T12:05:00.000000"}],
          "links": {}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &zoneExportListFlags{status: "COMPLETE", zoneID: "z1"}
	if err := runZoneExportList(context.Background(), dnsShareClient(fakeServer), o, f,
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneExportList error: %v", err)
	}
	if len(gotQueries) != 2 {
		t.Fatalf("requests = %d (%v), want 2 — links.next was not followed", len(gotQueries), gotQueries)
	}
	if !strings.Contains(gotQueries[0], "status=COMPLETE") || !strings.Contains(gotQueries[0], "zone_id=z1") {
		t.Errorf("first query = %q, want status and zone_id filters", gotQueries[0])
	}
	for _, want := range []string{"e1", "e2"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// --limit is a hard cap on results, not a page size, so a limit smaller than the
// first page must stop the walk before the second request.
func TestRunZoneExportList_LimitCapsResults(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	requests := 0
	fakeServer.Mux.HandleFunc("/zones/tasks/exports", func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "exports": [{"id": "e1", "zone_id": "z1", "status": "COMPLETE"},
                      {"id": "e2", "zone_id": "z2", "status": "COMPLETE"}],
          "links": {"next": "` + fakeServer.Server.URL + `/zones/tasks/exports?marker=e2"}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneExportList(context.Background(), dnsShareClient(fakeServer), o,
		&zoneExportListFlags{limit: 1}, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneExportList error: %v", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 — the limit should stop the page walk", requests)
	}
	if strings.Contains(buf.String(), "e2") {
		t.Errorf("output has e2 despite --limit 1\n---\n%s", buf.String())
	}
}

// The zonefile endpoint answers text/dns, not JSON.
func TestRunZoneExportShowFile_AcceptsTextDNS(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const zonefile = "$ORIGIN example.com.\nexample.com. 3600 IN SOA ns1.example.com. admin 1 60 60 60 60\n"
	var gotAccept string
	fakeServer.Mux.HandleFunc("/zones/tasks/exports/e1/export", func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/dns")
		_, _ = w.Write([]byte(zonefile))
	})

	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runZoneExportShowFile(context.Background(), dnsShareClient(fakeServer), o, "e1",
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneExportShowFile error: %v", err)
	}
	if gotAccept != "text/dns" {
		t.Errorf("Accept = %q, want text/dns", gotAccept)
	}
	// -f value must reproduce the zonefile byte-for-byte: the documented
	// workflow is `zone export showfile -f value > zone.txt` followed by
	// `zone import create`, and a flattened zonefile is not importable.
	if got := strings.TrimSuffix(buf.String(), "\n"); got != zonefile {
		t.Errorf("zonefile did not round-trip\n got: %q\nwant: %q", got, zonefile)
	}
}

func TestRunZoneExportDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/zones/tasks/exports/e1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runZoneExportDelete(context.Background(), dnsShareClient(fakeServer),
		[]string{"e1"}, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneExportDelete error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(buf.String(), "Deleted zone export e1") {
		t.Errorf("output = %q", buf.String())
	}
}

// A bare zonefile goes as text/dns; the JSON envelope is only for --attributes.
func TestRunZoneImportCreate_BareZonefileUsesTextDNS(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const zonefile = "example.com. 3600 IN SOA ns1.example.com. admin 1 60 60 60 60\n"
	var gotType, gotBody, gotMethod string
	fakeServer.Mux.HandleFunc("/zones/tasks/imports", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id": "i1", "status": "PENDING", "version": 1}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneImportCreate(context.Background(), dnsShareClient(fakeServer), o,
		zonefile, nil, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneImportCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotType != "text/dns" {
		t.Errorf("Content-Type = %q, want text/dns", gotType)
	}
	if gotBody != zonefile {
		t.Errorf("body = %q, want the zonefile verbatim", gotBody)
	}
	if !strings.Contains(buf.String(), "i1") {
		t.Errorf("output missing the import id\n---\n%s", buf.String())
	}
}

func TestRunZoneImportCreate_AttributesUseJSONEnvelope(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/imports", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{
          "zonefile": "example.com. 3600 IN SOA ns1.example.com. admin 1 60 60 60 60\n",
          "attributes": {"pool_level": "gold"}
        }`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id": "i1", "status": "PENDING"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneImportCreate(context.Background(), dnsShareClient(fakeServer), o,
		"example.com. 3600 IN SOA ns1.example.com. admin 1 60 60 60 60\n",
		map[string]string{"pool_level": "gold"}, &commonOptions{}, &buf)
	if err != nil {
		t.Fatalf("runZoneImportCreate error: %v", err)
	}
}

func TestParseZoneAttributes(t *testing.T) {
	got, err := parseZoneAttributes([]string{"pool_level:gold", "note:a:b"})
	if err != nil {
		t.Fatalf("parseZoneAttributes error: %v", err)
	}
	// Only the first colon separates, so a value may itself contain colons.
	if got["pool_level"] != "gold" || got["note"] != "a:b" {
		t.Errorf("attributes = %v", got)
	}
	if _, err := parseZoneAttributes([]string{"nocolon"}); err == nil {
		t.Error("parseZoneAttributes accepted a value with no colon")
	}
}

// The command reads the zonefile off disk before it authenticates, so a bad path
// must fail with the read error rather than an auth error.
func TestZoneImportCreateCommand_MissingZonefile(t *testing.T) {
	cmd := newZoneImportCreateCommand(&auth.Options{}, &output.Options{Format: output.FormatTable})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{filepath.Join(t.TempDir(), "missing.zone")})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for a missing zonefile")
	}
	if !strings.Contains(err.Error(), "reading zonefile") {
		t.Errorf("error = %v, want the zonefile read error", err)
	}
}

func TestRunZoneImportList_FiltersOnMessage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/zones/tasks/imports", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "imports": [{"id": "i1", "zone_id": "z1", "status": "ERROR", "message": "bad SOA"}],
          "links": {}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &zoneImportListFlags{status: "ERROR", message: "bad SOA"}
	if err := runZoneImportList(context.Background(), dnsShareClient(fakeServer), o, f,
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneImportList error: %v", err)
	}
	for _, want := range []string{"status=ERROR", "message=bad+SOA"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query = %q, want %q", gotQuery, want)
		}
	}
	if !strings.Contains(buf.String(), "bad SOA") {
		t.Errorf("output missing the message column\n---\n%s", buf.String())
	}
}

func TestRunZoneImportShow_AndDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethods []string
	fakeServer.Mux.HandleFunc("/zones/tasks/imports/i1", func(w http.ResponseWriter, r *http.Request) {
		gotMethods = append(gotMethods, r.Method)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "i1", "zone_id": "z1", "status": "COMPLETE", "version": 1}`))
	})

	o := &output.Options{Format: output.FormatTable}
	client := dnsShareClient(fakeServer)
	var buf bytes.Buffer
	if err := runZoneImportShow(context.Background(), client, o, "i1", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneImportShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "COMPLETE") {
		t.Errorf("output missing the status\n---\n%s", buf.String())
	}
	buf.Reset()
	if err := runZoneImportDelete(context.Background(), client, []string{"i1"}, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneImportDelete error: %v", err)
	}
	if strings.Join(gotMethods, ",") != "GET,DELETE" {
		t.Errorf("methods = %v, want GET then DELETE", gotMethods)
	}
}

// The designate admin options travel as headers, not query parameters.
func TestCommonOptions_Headers(t *testing.T) {
	if h := (&commonOptions{}).headers(); h != nil {
		t.Errorf("headers = %v, want nil when neither option is given", h)
	}
	h := (&commonOptions{allProjects: true, sudoProjectID: "p9"}).headers()
	if h["X-Auth-All-Projects"] != "true" || h["X-Auth-Sudo-Project-ID"] != "p9" {
		t.Errorf("headers = %v", h)
	}
}

func TestRunZoneExportList_SendsCommonHeaders(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotAll, gotSudo, gotQuery string
	fakeServer.Mux.HandleFunc("/zones/tasks/exports", func(w http.ResponseWriter, r *http.Request) {
		gotAll = r.Header.Get("X-Auth-All-Projects")
		gotSudo = r.Header.Get("X-Auth-Sudo-Project-ID")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"exports": [], "links": {}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	common := &commonOptions{allProjects: true, sudoProjectID: "p9"}
	if err := runZoneExportList(context.Background(), dnsShareClient(fakeServer), o,
		&zoneExportListFlags{}, common, &buf); err != nil {
		t.Fatalf("runZoneExportList error: %v", err)
	}
	if gotAll != "true" || gotSudo != "p9" {
		t.Errorf("headers: all-projects=%q sudo=%q", gotAll, gotSudo)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty — these are headers, not query parameters", gotQuery)
	}
}
