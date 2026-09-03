package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// TestServerList_LongCarriesOwner covers the Project ID / User ID columns.
// Attributing a host's guests to their owners is the first step of a drain, and
// nova returns both in /servers/detail at every microversion, so --long should
// not make the operator run a query per project to get them.
func TestServerList_LongCarriesOwner(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[{
		  "id": "11111111-1111-1111-1111-111111111111",
		  "name": "web-1",
		  "status": "ACTIVE",
		  "addresses": {},
		  "flavor": {"original_name": "m1.small"},
		  "image": {"id": "img-123"},
		  "tenant_id": "proj-9",
		  "user_id": "user-4",
		  "OS-EXT-SRV-ATTR:host": "compute-7"
		}]}`))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{long: true}, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Project ID", "User ID", "proj-9", "user-4"} {
		if !strings.Contains(out, want) {
			t.Errorf("server list --long output missing %q\n---\n%s", want, out)
		}
	}

	// The default listing stays as it was: these are --long columns.
	buf.Reset()
	if err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{}, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	if strings.Contains(buf.String(), "Project ID") {
		t.Errorf("default listing unexpectedly carries Project ID\n---\n%s", buf.String())
	}
}

// TestRunComputeServiceList_DisabledReasonWithoutLong covers the read-back
// side of "compute service set --disable-reason": the reason a host was
// disabled — by an operator, or by an HA agent or autoevacuator — appears
// without --long, because a disabled fleet member is exactly when it matters.
// A fleet with nothing disabled keeps the vanilla column set.
func TestRunComputeServiceList_DisabledReasonWithoutLong(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   []string
		absent []string
	}{
		{
			name: "a disabled service brings the column",
			body: `{"services":[
			  {"id":"1","binary":"nova-compute","host":"compute-1","zone":"nova","status":"enabled","state":"up"},
			  {"id":"2","binary":"nova-compute","host":"compute-2","zone":"nova","status":"disabled",
			   "state":"up","disabled_reason":"autoevacuator: host fenced"}
			]}`,
			want: []string{"Disabled Reason", "autoevacuator: host fenced"},
			// Forced Down stays a --long column.
			absent: []string{"Forced Down"},
		},
		{
			name: "nothing disabled, nothing added",
			body: `{"services":[
			  {"id":"1","binary":"nova-compute","host":"compute-1","zone":"nova","status":"enabled","state":"up"}
			]}`,
			absent: []string{"Disabled Reason", "Forced Down", "Admin State"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})

			o := &output.Options{Format: output.FormatCSV}
			var buf bytes.Buffer
			if err := runComputeServiceList(context.Background(), computeClient(fakeServer, "2.79"), o,
				&serviceListFlags{}, &buf); err != nil {
				t.Fatalf("runComputeServiceList: %v", err)
			}
			out := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("service list output missing %q\n---\n%s", want, out)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("service list output unexpectedly has %q\n---\n%s", absent, out)
				}
			}
		})
	}
}

// The KeyStack admin_state/error_details columns are no longer gated on --long
// either: vanilla nova does not return the fields at all, so their presence in
// the response is the signal, and the state an HA agent left behind is visible
// in the plain listing.
func TestRunComputeServiceList_KeyStackAdminStateWithoutLong(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(serviceListBodyKeyStack))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runComputeServiceList(context.Background(), computeClient(fakeServer, "2.79"), o,
		&serviceListFlags{}, &buf); err != nil {
		t.Fatalf("runComputeServiceList: %v", err)
	}
	for _, want := range []string{"Admin State", "Error Details", "Error", "disk failure"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("keystack service list output missing %q\n---\n%s", want, buf.String())
		}
	}
}
