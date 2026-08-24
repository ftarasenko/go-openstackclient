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

func TestRunZoneBlacklistList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotQuery string
	fakeServer.Mux.HandleFunc("/blacklists", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "blacklists": [
            {"id": "b1", "pattern": "^example\\.com\\.$", "description": "reserved"}
          ],
          "links": {}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneBlacklistList(context.Background(), dnsShareClient(fakeServer), o,
		`^example\.com\.$`, 0, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneBlacklistList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if !strings.Contains(gotQuery, "pattern=") {
		t.Errorf("query = %q, want a pattern filter", gotQuery)
	}
	for _, want := range []string{"b1", "reserved"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunZoneBlacklistCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/blacklists", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"pattern": "^bad\\.example\\.com\\.$", "description": "no"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "b1", "pattern": "^bad\\.example\\.com\\.$", "description": "no"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneBlacklistCreate(context.Background(), dnsShareClient(fakeServer), o,
		`^bad\.example\.com\.$`, "no", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneBlacklistCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "b1") {
		t.Errorf("output missing the new id\n---\n%s", buf.String())
	}
}

// Designate updates with PATCH, and clearing the description needs an explicit
// null — an omitted key would leave the old value in place.
func TestRunZoneBlacklistSet_NoDescriptionSendsNull(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// Addressed by ID, so no list lookup happens first.
	const id = "3a1f9c72-58b4-4e0d-8f21-6c7d9e0a1b23"
	var gotMethod string
	fakeServer.Mux.HandleFunc("/blacklists/"+id, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "Content-Type", "application/json")
		th.TestJSONRequest(t, r, `{"description": null}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "` + id + `", "pattern": "^x$", "description": ""}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneBlacklistSet(context.Background(), dnsShareClient(fakeServer), o, id,
		&blacklistSetFlags{noDescription: true, common: &commonOptions{}}, &buf); err != nil {
		t.Fatalf("runZoneBlacklistSet error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
}

// A blacklist may be addressed by its pattern; that costs a list lookup first.
func TestRunZoneBlacklistDelete_ResolvesPattern(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/blacklists", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"blacklists": [{"id": "b1", "pattern": "^x$"}], "links": {}}`))
	})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/blacklists/b1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runZoneBlacklistDelete(context.Background(), dnsShareClient(fakeServer),
		[]string{"^x$"}, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneBlacklistDelete error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(buf.String(), "Deleted blacklist ^x$") {
		t.Errorf("output = %q", buf.String())
	}
}

// A UUID reference must not cost a list call: the resolver returns it untouched.
func TestResolveBlacklistID_UUIDPassthrough(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No handlers registered — any request would fail the test with a 404 error.

	const id = "8f2b3d5a-1e6c-4a7b-9c0d-2e3f4a5b6c7d"
	got, err := resolveBlacklistID(context.Background(), dnsShareClient(fakeServer), id, nil)
	if err != nil {
		t.Fatalf("resolveBlacklistID error: %v", err)
	}
	if got != id {
		t.Errorf("id = %q, want %q", got, id)
	}
}

func TestRunTLDList_AndShowByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/tlds", func(w http.ResponseWriter, r *http.Request) {
		if want := "name=ru"; r.URL.RawQuery != "" && !strings.Contains(r.URL.RawQuery, want) {
			t.Errorf("query = %q, want %q", r.URL.RawQuery, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "tlds": [{"id": "t1", "name": "ru", "description": "Russia"}],
          "links": {}
        }`))
	})
	fakeServer.Mux.HandleFunc("/tlds/t1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "t1", "name": "ru", "description": "Russia",
          "created_at": "2026-08-06T12:00:00.000000"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	client := dnsShareClient(fakeServer)
	var buf bytes.Buffer
	if err := runTLDList(context.Background(), client, o, "ru", 0, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runTLDList error: %v", err)
	}
	if !strings.Contains(buf.String(), "Russia") {
		t.Errorf("output missing the description\n---\n%s", buf.String())
	}
	// Upstream resolves a TLD name to its ID, so "show ru" must hit /tlds/t1.
	buf.Reset()
	if err := runTLDShow(context.Background(), client, o, "ru", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runTLDShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "t1") {
		t.Errorf("output missing the id\n---\n%s", buf.String())
	}
}

func TestRunTLDCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/tlds", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"name": "ru", "description": "Russia"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "t1", "name": "ru", "description": "Russia"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTLDCreate(context.Background(), dnsShareClient(fakeServer), o,
		"ru", "Russia", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runTLDCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
}

func TestRunTLDSet_PatchesNameOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "5c2e8b41-77a3-4f19-b0d6-8e1a2c3d4f50"
	var gotMethod string
	fakeServer.Mux.HandleFunc("/tlds/"+id, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		// Only the named attribute is sent, so the description survives untouched.
		th.TestJSONRequest(t, r, `{"name": "su"}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "` + id + `", "name": "su", "description": "Russia"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTLDSet(context.Background(), dnsShareClient(fakeServer), o, id,
		&tldSetFlags{name: "su", common: &commonOptions{}}, &buf); err != nil {
		t.Fatalf("runTLDSet error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if !strings.Contains(buf.String(), "su") {
		t.Errorf("output missing the new name\n---\n%s", buf.String())
	}
}

func TestRunTLDDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "8f2b3d5a-1e6c-4a7b-9c0d-2e3f4a5b6c7d"
	var gotMethod string
	fakeServer.Mux.HandleFunc("/tlds/"+id, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runTLDDelete(context.Background(), dnsShareClient(fakeServer),
		[]string{id}, &commonOptions{}, &buf); err != nil {
		t.Fatalf("runTLDDelete error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
}

// A flagless "set" must be rejected before any request goes out.
func TestSetCommands_RejectEmptyUpdate(t *testing.T) {
	a := &auth.Options{}
	o := &output.Options{Format: output.FormatTable}
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{"zone blacklist set", func() *cobra.Command { return newZoneBlacklistSetCommand(a, o) }},
		{"tld set", func() *cobra.Command { return newTLDSetCommand(a, o) }},
	} {
		cmd := tc.cmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		cmd.SetArgs([]string{"x"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "nothing to set") {
			t.Errorf("%s: error = %v, want a \"nothing to set\" error", tc.name, err)
		}
	}
}

// --description and --no-description contradict each other, so cobra must refuse
// the combination rather than letting the later branch win silently.
func TestSetCommands_DescriptionFlagsAreExclusive(t *testing.T) {
	a := &auth.Options{}
	o := &output.Options{Format: output.FormatTable}
	for _, cmd := range []*cobra.Command{
		newZoneBlacklistSetCommand(a, o),
		newTLDSetCommand(a, o),
	} {
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		cmd.SetArgs([]string{"x", "--description", "a", "--no-description"})
		if err := cmd.Execute(); err == nil {
			t.Errorf("%s accepted both --description and --no-description", cmd.Name())
		}
	}
}
