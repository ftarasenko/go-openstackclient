package dns

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --- zone transfer request: list/show/set/delete ---------------------------

const transferRequestListBody = `{
  "transfer_requests": [
    {
      "id": "tr1", "zone_id": "z1", "zone_name": "example.com.",
      "project_id": "p1", "target_project_id": "p2", "status": "ACTIVE"
    }
  ]
}`

func TestRunZoneTransferRequestList_RequestAndTable(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotStatus string
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotStatus = r.URL.Query().Get("status")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(transferRequestListBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneTransferRequestList(context.Background(), dnsClient(fakeServer), o, "ACTIVE", &buf); err != nil {
		t.Fatalf("runZoneTransferRequestList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotStatus != "ACTIVE" {
		t.Errorf("?status = %q, want ACTIVE", gotStatus)
	}
	out := buf.String()
	for _, want := range []string{"tr1", "z1", "example.com.", "p2", "ACTIVE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunZoneTransferRequestList_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferRequestList(context.Background(), dnsClient(fakeServer), o, "", &buf)
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}
	if !strings.Contains(err.Error(), "listing zone transfer requests") {
		t.Errorf("error = %v, want it to name the operation", err)
	}
}

const transferRequestShowBody = `{
  "id": "tr1", "zone_id": "z1", "zone_name": "example.com.",
  "project_id": "p1", "target_project_id": "p2", "key": "SECRETKEY",
  "description": "handing over", "status": "ACTIVE"
}`

func TestRunZoneTransferRequestShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/tr1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(transferRequestShowBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneTransferRequestShow(context.Background(), dnsClient(fakeServer), o, "tr1", &buf); err != nil {
		t.Fatalf("runZoneTransferRequestShow error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/zones/tasks/transfer_requests/tr1" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(buf.String(), "SECRETKEY") {
		t.Errorf("output missing the transfer key\n---\n%s", buf.String())
	}
}

func TestRunZoneTransferRequestShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferRequestShow(context.Background(), dnsClient(fakeServer), o, "missing", &buf)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %v, want it to name the transfer request", err)
	}
}

// runZoneTransferRequestSet sends both fields with UpdateOpts tagged omitempty,
// so leaving one flag empty must drop that key from the body entirely rather
// than sending an empty string that would clear it server-side.
func TestRunZoneTransferRequestSet_SparseBody(t *testing.T) {
	tests := []struct {
		name            string
		targetProjectID string
		description     string
		wantBody        string
	}{
		{name: "description only", targetProjectID: "", description: "new description",
			wantBody: `{"description": "new description"}`},
		{name: "target project only", targetProjectID: "p3", description: "",
			wantBody: `{"target_project_id": "p3"}`},
		{name: "both", targetProjectID: "p3", description: "new description",
			wantBody: `{"target_project_id": "p3", "description": "new description"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod string
			fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/tr1", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				th.TestJSONRequest(t, r, tc.wantBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(transferRequestShowBody))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			err := runZoneTransferRequestSet(context.Background(), dnsClient(fakeServer), o, "tr1",
				tc.targetProjectID, tc.description, &buf)
			if err != nil {
				t.Fatalf("runZoneTransferRequestSet error: %v", err)
			}
			if gotMethod != http.MethodPatch {
				t.Errorf("method = %q, want PATCH", gotMethod)
			}
		})
	}
}

func TestRunZoneTransferRequestSet_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/tr1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferRequestSet(context.Background(), dnsClient(fakeServer), o, "tr1", "p3", "", &buf)
	if err == nil {
		t.Fatal("expected an error from a 400 response")
	}
	if !strings.Contains(err.Error(), "tr1") {
		t.Errorf("error = %v, want it to name the transfer request", err)
	}
}

func TestRunZoneTransferRequestDelete_AllSucceed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	for _, id := range []string{"tr1", "tr2"} {
		fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/"+id, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodDelete)
			deleted = append(deleted, id)
			w.WriteHeader(http.StatusNoContent)
		})
	}

	var buf bytes.Buffer
	err := runZoneTransferRequestDelete(context.Background(), dnsClient(fakeServer), []string{"tr1", "tr2"}, &buf)
	if err != nil {
		t.Fatalf("runZoneTransferRequestDelete error: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleted %v, want both requests removed", deleted)
	}
	for _, want := range []string{"Deleted zone transfer request tr1", "Deleted zone transfer request tr2"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// batchdelete.Each must attempt every ref even when an earlier one fails, and
// the returned error must name the one that failed while still reporting the
// one that succeeded.
func TestRunZoneTransferRequestDelete_PartialFailureContinues(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var attempted []string
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/bad", func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, "bad")
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_requests/good", func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, "good")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runZoneTransferRequestDelete(context.Background(), dnsClient(fakeServer), []string{"bad", "good"}, &buf)
	if err == nil {
		t.Fatal("expected a joined error naming the failed delete")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error = %v, want it to name the failed transfer request", err)
	}
	if len(attempted) != 2 {
		t.Errorf("attempted = %v, want both refs attempted despite the failure", attempted)
	}
	if !strings.Contains(buf.String(), "Deleted zone transfer request good") {
		t.Errorf("output missing confirmation for the successful delete:\n%s", buf.String())
	}
}

// --- zone transfer accept: list/show ----------------------------------------

const transferAcceptListBody = `{
  "transfer_accepts": [
    {"id": "ta1", "zone_id": "z1", "project_id": "p2",
     "zone_transfer_request_id": "tr1", "status": "COMPLETE"}
  ]
}`

func TestRunZoneTransferAcceptList_RequestAndTable(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotStatus string
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_accepts", func(w http.ResponseWriter, r *http.Request) {
		gotStatus = r.URL.Query().Get("status")
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(transferAcceptListBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneTransferAcceptList(context.Background(), dnsClient(fakeServer), o, "COMPLETE", &buf); err != nil {
		t.Fatalf("runZoneTransferAcceptList error: %v", err)
	}
	if gotStatus != "COMPLETE" {
		t.Errorf("?status = %q, want COMPLETE", gotStatus)
	}
	out := buf.String()
	for _, want := range []string{"ta1", "z1", "p2", "tr1", "COMPLETE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunZoneTransferAcceptList_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_accepts", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferAcceptList(context.Background(), dnsClient(fakeServer), o, "", &buf)
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}
}

const transferAcceptShowBody = `{
  "id": "ta1", "status": "COMPLETE", "project_id": "p2", "zone_id": "z1",
  "zone_transfer_request_id": "tr1"
}`

func TestRunZoneTransferAcceptShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_accepts/ta1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(transferAcceptShowBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneTransferAcceptShow(context.Background(), dnsClient(fakeServer), o, "ta1", &buf); err != nil {
		t.Fatalf("runZoneTransferAcceptShow error: %v", err)
	}
	if gotPath != "/zones/tasks/transfer_accepts/ta1" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(buf.String(), "COMPLETE") {
		t.Errorf("output missing the accept status\n---\n%s", buf.String())
	}
}

func TestRunZoneTransferAcceptShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_accepts/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferAcceptShow(context.Background(), dnsClient(fakeServer), o, "missing", &buf)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}

// --- TSIG key: set/delete ----------------------------------------------------

// runTSIGKeySet relies on UpdateOpts tagging every field omitempty, so a wrong
// branch here would silently send or drop a field designate should not see.
func TestRunTSIGKeySet_SparseBody(t *testing.T) {
	tests := []struct {
		name     string
		flags    *tsigKeyWriteFlags
		wantBody string
	}{
		{name: "secret only", flags: &tsigKeyWriteFlags{secret: "NEWSECRET"},
			wantBody: `{"secret": "NEWSECRET"}`},
		{name: "name and scope", flags: &tsigKeyWriteFlags{name: "renamed", scope: "ZONE"},
			wantBody: `{"name": "renamed", "scope": "ZONE"}`},
	}
	// A UUID-shaped ref short-circuits resolveTSIGKeyID's name lookup, so this
	// exercises the PATCH body directly; name resolution is covered separately
	// in TestRunTSIGKeySet_ResolvesNameToID.
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod string
			fakeServer.Mux.HandleFunc("/tsigkeys/"+id, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				th.TestJSONRequest(t, r, tc.wantBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tsigKeyBody))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			if err := runTSIGKeySet(context.Background(), dnsClient(fakeServer), o, id, tc.flags, &buf); err != nil {
				t.Fatalf("runTSIGKeySet error: %v", err)
			}
			if gotMethod != http.MethodPatch {
				t.Errorf("method = %q, want PATCH", gotMethod)
			}
		})
	}
}

// A non-UUID ref must resolve through the name lookup before the PATCH.
func TestRunTSIGKeySet_ResolvesNameToID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotListName string
	fakeServer.Mux.HandleFunc("/tsigkeys", func(w http.ResponseWriter, r *http.Request) {
		gotListName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tsigkeys": [` + tsigKeyBody + `]}`))
	})
	var gotPath string
	fakeServer.Mux.HandleFunc("/tsigkeys/k1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tsigKeyBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &tsigKeyWriteFlags{secret: "NEWSECRET"}
	if err := runTSIGKeySet(context.Background(), dnsClient(fakeServer), o, "transfer-key", f, &buf); err != nil {
		t.Fatalf("runTSIGKeySet error: %v", err)
	}
	if gotListName != "transfer-key" {
		t.Errorf("name lookup ?name= = %q, want transfer-key", gotListName)
	}
	if gotPath != "/tsigkeys/k1" {
		t.Errorf("PATCH path = %q, want resolved ID /tsigkeys/k1", gotPath)
	}
}

func TestRunTSIGKeySet_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	fakeServer.Mux.HandleFunc("/tsigkeys/"+id, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runTSIGKeySet(context.Background(), dnsClient(fakeServer), o, id, &tsigKeyWriteFlags{secret: "X"}, &buf)
	if err == nil {
		t.Fatal("expected an error from a 400 response")
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("error = %v, want it to name the key", err)
	}
}

func TestRunTSIGKeyDelete_AllSucceed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	ids := []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	var deleted []string
	for _, id := range ids {
		fakeServer.Mux.HandleFunc("/tsigkeys/"+id, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodDelete)
			deleted = append(deleted, id)
			w.WriteHeader(http.StatusNoContent)
		})
	}

	var buf bytes.Buffer
	err := runTSIGKeyDelete(context.Background(), dnsClient(fakeServer), ids, &buf)
	if err != nil {
		t.Fatalf("runTSIGKeyDelete error: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleted %v, want both keys removed", deleted)
	}
	for _, id := range ids {
		if !strings.Contains(buf.String(), "Deleted TSIG key "+id) {
			t.Errorf("output missing confirmation for %s\n---\n%s", id, buf.String())
		}
	}
}

// batchdelete.Each must attempt every ref even when an earlier one fails.
func TestRunTSIGKeyDelete_PartialFailureContinues(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const badID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const goodID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	var attempted []string
	fakeServer.Mux.HandleFunc("/tsigkeys/"+badID, func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, badID)
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/tsigkeys/"+goodID, func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, goodID)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runTSIGKeyDelete(context.Background(), dnsClient(fakeServer), []string{badID, goodID}, &buf)
	if err == nil {
		t.Fatal("expected a joined error naming the failed delete")
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("error = %v, want it to name the failed key", err)
	}
	if len(attempted) != 2 {
		t.Errorf("attempted = %v, want both refs attempted despite the failure", attempted)
	}
	if !strings.Contains(buf.String(), "Deleted TSIG key "+goodID) {
		t.Errorf("output missing confirmation for the successful delete:\n%s", buf.String())
	}
}

// --- PTR record show ---------------------------------------------------------

const ptrRecordBody = `{
  "id": "RegionOne:11111111-1111-1111-1111-111111111111",
  "ptrdname": "host.example.com.", "description": "primary host",
  "address": "192.0.2.5", "ttl": 3600, "status": "ACTIVE", "action": "NONE"
}`

func TestRunPTRRecordShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	id := "RegionOne:11111111-1111-1111-1111-111111111111"
	var gotPath string
	fakeServer.Mux.HandleFunc("/reverse/floatingips/"+id, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ptrRecordBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPTRRecordShow(context.Background(), dnsClient(fakeServer), o, id, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runPTRRecordShow error: %v", err)
	}
	if gotPath != "/reverse/floatingips/"+id {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{"host.example.com.", "192.0.2.5", "primary host"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunPTRRecordShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	id := "RegionOne:22222222-2222-2222-2222-222222222222"
	fakeServer.Mux.HandleFunc("/reverse/floatingips/"+id, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runPTRRecordShow(context.Background(), dnsClient(fakeServer), o, id, &commonOptions{}, &buf)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("error = %v, want it to name the floating IP", err)
	}
}

func TestRunPTRRecordShow_AllProjectsUsesHeader(t *testing.T) {
	id := "RegionOne:33333333-3333-3333-3333-333333333333"
	tests := []struct {
		name        string
		allProjects bool
		wantHeader  string
	}{
		{name: "default", allProjects: false, wantHeader: ""},
		{name: "--all-projects", allProjects: true, wantHeader: "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotHeader string
			fakeServer.Mux.HandleFunc("/reverse/floatingips/"+id, func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("X-Auth-All-Projects")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(ptrRecordBody))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			common := &commonOptions{allProjects: tc.allProjects}
			err := runPTRRecordShow(context.Background(), dnsShareClient(fakeServer), o, id, common, &buf)
			if err != nil {
				t.Fatalf("runPTRRecordShow error: %v", err)
			}
			if gotHeader != tc.wantHeader {
				t.Errorf("X-Auth-All-Projects = %q, want %q", gotHeader, tc.wantHeader)
			}
		})
	}
}

// --- zone blacklist show -----------------------------------------------------

const blacklistBody = `{
  "id": "bl1", "pattern": "^blocked\\..*$", "description": "blocked TLD",
  "created_at": "2026-08-06T12:00:00.000000"
}`

func TestRunZoneBlacklistShow_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// A UUID-shaped ref short-circuits resolveBlacklistID's pattern lookup.
	const id = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	var gotPath string
	fakeServer.Mux.HandleFunc("/blacklists/"+id, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(blacklistBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneBlacklistShow(context.Background(), dnsClient(fakeServer), o, id, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneBlacklistShow error: %v", err)
	}
	if gotPath != "/blacklists/"+id {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(buf.String(), "blocked TLD") {
		t.Errorf("output missing description\n---\n%s", buf.String())
	}
}

// resolveBlacklistID lists by pattern when given a non-UUID ref, and a single
// match resolves to that entry's ID.
func TestRunZoneBlacklistShow_ResolvesPatternToID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPattern string
	fakeServer.Mux.HandleFunc("/blacklists", func(w http.ResponseWriter, r *http.Request) {
		gotPattern = r.URL.Query().Get("pattern")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"blacklists": [` + blacklistBody + `]}`))
	})
	var gotPath string
	fakeServer.Mux.HandleFunc("/blacklists/bl1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(blacklistBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	ref := `^blocked\..*$`
	if err := runZoneBlacklistShow(context.Background(), dnsClient(fakeServer), o, ref, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneBlacklistShow error: %v", err)
	}
	if gotPattern != ref {
		t.Errorf("?pattern = %q, want %q", gotPattern, ref)
	}
	if gotPath != "/blacklists/bl1" {
		t.Errorf("resolved show path = %q, want /blacklists/bl1", gotPath)
	}
}

// A pattern matching more than one blacklist must be rejected rather than
// silently picking one.
func TestRunZoneBlacklistShow_AmbiguousPatternErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/blacklists", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"blacklists": [
          {"id": "bl1", "pattern": "blocked1", "description": "one"},
          {"id": "bl2", "pattern": "blocked2", "description": "two"}
        ]}`))
	})
	fakeServer.Mux.HandleFunc("/blacklists/bl1", func(_ http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Fatal("ambiguous pattern must not resolve to a show request")
		}
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneBlacklistShow(context.Background(), dnsClient(fakeServer), o, "blocked", &commonOptions{}, &buf)
	if err == nil {
		t.Fatal("expected an ambiguous-pattern error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %v, want it to report the ambiguity", err)
	}
}

func TestRunZoneBlacklistShow_AllProjectsUsesHeader(t *testing.T) {
	tests := []struct {
		name        string
		allProjects bool
		wantHeader  string
	}{
		{name: "default", allProjects: false, wantHeader: ""},
		{name: "--all-projects", allProjects: true, wantHeader: "true"},
	}
	const id = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotHeader string
			fakeServer.Mux.HandleFunc("/blacklists/"+id, func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("X-Auth-All-Projects")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(blacklistBody))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			common := &commonOptions{allProjects: tc.allProjects}
			err := runZoneBlacklistShow(context.Background(), dnsShareClient(fakeServer), o, id, common, &buf)
			if err != nil {
				t.Fatalf("runZoneBlacklistShow error: %v", err)
			}
			if gotHeader != tc.wantHeader {
				t.Errorf("X-Auth-All-Projects = %q, want %q", gotHeader, tc.wantHeader)
			}
		})
	}
}

// --- zone export show ---------------------------------------------------------

const zoneExportBody = `{
  "id": "exp1", "zone_id": "z1", "project_id": "p1", "status": "COMPLETE",
  "location": "designate://v2/zones/tasks/exports/exp1/export", "version": 1,
  "created_at": "2026-08-06T12:00:00.000000"
}`

func TestRunZoneExportShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/zones/tasks/exports/exp1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(zoneExportBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneExportShow(context.Background(), dnsClient(fakeServer), o, "exp1", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneExportShow error: %v", err)
	}
	if gotPath != "/zones/tasks/exports/exp1" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{"exp1", "z1", "COMPLETE"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunZoneExportShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/zones/tasks/exports/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneExportShow(context.Background(), dnsClient(fakeServer), o, "missing", &commonOptions{}, &buf)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %v, want it to name the export", err)
	}
}

func TestRunZoneExportShow_AllProjectsUsesHeader(t *testing.T) {
	tests := []struct {
		name        string
		allProjects bool
		wantHeader  string
	}{
		{name: "default", allProjects: false, wantHeader: ""},
		{name: "--all-projects", allProjects: true, wantHeader: "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotHeader string
			fakeServer.Mux.HandleFunc("/zones/tasks/exports/exp1", func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("X-Auth-All-Projects")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(zoneExportBody))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			common := &commonOptions{allProjects: tc.allProjects}
			err := runZoneExportShow(context.Background(), dnsShareClient(fakeServer), o, "exp1", common, &buf)
			if err != nil {
				t.Fatalf("runZoneExportShow error: %v", err)
			}
			if gotHeader != tc.wantHeader {
				t.Errorf("X-Auth-All-Projects = %q, want %q", gotHeader, tc.wantHeader)
			}
		})
	}
}
