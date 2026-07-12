package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// TestConsoleTypeFromFlags exercises the discrete console flag mapping (N3):
// defaults to noVNC, honors each discrete flag, falls back to --type, and
// rejects more than one discrete flag.
func TestConsoleTypeFromFlags(t *testing.T) {
	cases := []struct {
		name                              string
		novnc, xvpvnc, spice, serial, mks bool
		consoleType                       string
		want                              string
		wantErr                           bool
	}{
		{name: "default", want: "novnc"},
		{name: "novnc", novnc: true, want: "novnc"},
		{name: "xvpvnc", xvpvnc: true, want: "xvpvnc"},
		{name: "spice", spice: true, want: "spice-html5"},
		{name: "serial", serial: true, want: "serial"},
		{name: "mks", mks: true, want: "webmks"},
		{name: "type-alias", consoleType: "serial", want: "serial"},
		{name: "discrete-wins-over-type", spice: true, consoleType: "serial", want: "spice-html5"},
		{name: "mutually-exclusive", novnc: true, spice: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := consoleTypeFromFlags(tc.novnc, tc.xvpvnc, tc.spice, tc.serial, tc.mks, tc.consoleType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got type %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("consoleTypeFromFlags = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunConsoleURLShow_SpiceProtocol confirms the resolved console type reaches
// nova with the matching protocol (spice-html5 → spice).
func TestRunConsoleURLShow_SpiceProtocol(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+id+"/remote-consoles", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"remote_console": {"protocol": "spice", "type": "spice-html5", "url": "http://spice.example/x"}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runConsoleURLShow(context.Background(), client, o, id, "spice-html5", &buf); err != nil {
		t.Fatalf("runConsoleURLShow returned error: %v", err)
	}
	rc, _ := gotBody["remote_console"].(map[string]any)
	if rc["protocol"] != "spice" || rc["type"] != "spice-html5" {
		t.Errorf("request body remote_console = %v, want protocol=spice type=spice-html5", rc)
	}
	if !strings.Contains(buf.String(), "http://spice.example/x") {
		t.Errorf("output missing console URL:\n%s", buf.String())
	}
}

// TestResolveFlavorRef_AmbiguousName ensures a duplicate flavor name is rejected
// rather than silently resolving to the first match (M2).
func TestResolveFlavorRef_AmbiguousName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavors": [
			{"id": "a1", "name": "dup"},
			{"id": "b2", "name": "dup"}
		]}`))
	})

	client := computeClient(fakeServer, "latest")
	_, err := resolveFlavorRef(context.Background(), client, "dup")
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "more than one flavor named") {
		t.Errorf("error = %q, want ambiguity message", err.Error())
	}
}

// TestResolveFlavorRef_ExactIDWins ensures an exact ID match is preferred and
// unambiguous even when a different flavor shares the name.
func TestResolveFlavorRef_ExactIDWins(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavors": [
			{"id": "1", "name": "m1.small"},
			{"id": "2", "name": "m1.large"}
		]}`))
	})

	client := computeClient(fakeServer, "latest")
	got, err := resolveFlavorRef(context.Background(), client, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1" {
		t.Errorf("resolveFlavorRef = %q, want %q", got, "1")
	}
}

// TestRunServerList_LimitAndMarker checks that --limit is passed as a page size
// AND enforced as a hard result cap, and that --marker is forwarded (M4).
func TestRunServerList_LimitAndMarker(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"limit":  "1",
			"marker": "00000000-0000-0000-0000-000000000000",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Nova may return more than the requested limit (limit is a page size).
		_, _ = w.Write([]byte(serverListBody))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &serverListFlags{limit: 1, marker: "00000000-0000-0000-0000-000000000000"}

	var buf bytes.Buffer
	if err := runServerList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "web-1") {
		t.Errorf("output missing first server:\n%s", out)
	}
	if strings.Contains(out, "web-2") {
		t.Errorf("--limit=1 should truncate to one row, but web-2 present:\n%s", out)
	}
}

// TestRunQuotaShow_UsesProjectIDInURL confirms the (already-resolved) project ID
// is what nova receives on the quotasets URL — the fix for M1 resolves a name to
// this ID before reaching the seam.
func TestRunQuotaShow_UsesProjectIDInURL(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const projectID = "abcabcabcabc0000abcabcabcabc0000"
	fakeServer.Mux.HandleFunc("/os-quota-sets/"+projectID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"quota_set": {"id": "` + projectID + `", "instances": 10, "cores": 20, "ram": 51200}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runQuotaShow(context.Background(), client, o, projectID, false, &buf); err != nil {
		t.Fatalf("runQuotaShow returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "10") {
		t.Errorf("output missing instances quota:\n%s", buf.String())
	}
}
