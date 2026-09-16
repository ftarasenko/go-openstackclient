package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const clusterListBody = `{"clusters": [
  {"name": "cluster_01@rbd-1", "binary": "cinder-volume", "state": "up", "status": "enabled"},
  {"name": "cluster_01@hpe-3par", "binary": "cinder-volume", "state": "down", "status": "disabled"}
]}`

const clusterDetailBody = `{"clusters": [
  {"name": "cluster_01@rbd-1", "binary": "cinder-volume", "state": "up", "status": "enabled",
   "num_hosts": 3, "num_down_hosts": 0, "last_heartbeat": "2026-09-16T10:49:09.000000",
   "disabled_reason": null, "created_at": "2026-01-02T03:04:05.000000",
   "updated_at": "2026-09-16T10:49:09.000000"}
]}`

const clusterShowBody = `{"cluster": {
  "name": "cluster_01@rbd-1", "binary": "cinder-volume", "state": "up", "status": "disabled",
  "disabled_reason": "backend drained", "num_hosts": 3, "num_down_hosts": 1,
  "last_heartbeat": "2026-09-16T10:49:09.000000", "created_at": "2026-01-02T03:04:05.000000",
  "updated_at": "2026-09-16T10:49:12.000000", "replication_status": "enabled",
  "frozen": false, "active_backend_id": "rbd-1"
}}`

// The summary listing is /clusters; --long switches to /clusters/detail, since
// every column it adds exists only in the detailed response.
func TestRunClusterList_LongSwitchesEndpointAndColumns(t *testing.T) {
	tests := []struct {
		name     string
		long     bool
		path     string
		body     string
		want     []string
		unwanted []string
	}{
		{
			name:     "summary",
			path:     "/clusters",
			body:     clusterListBody,
			want:     []string{"Name", "Binary", "State", "Status", "cluster_01@rbd-1", "cluster_01@hpe-3par"},
			unwanted: []string{"Num Hosts", "Last Heartbeat", "Created At"},
		},
		{
			name: "long",
			long: true,
			path: "/clusters/detail",
			body: clusterDetailBody,
			want: []string{
				"Num Hosts", "Num Down Hosts", "Last Heartbeat", "Disabled Reason",
				"Created At", "Updated At", "2026-09-16T10:49:09.000000",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotPath string
			fakeServer.Mux.HandleFunc(tc.path, func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})

			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatTable}
			f := &clusterListFlags{long: tc.long}
			if err := runClusterList(context.Background(), volumeClient(fakeServer, "latest"), o, f, &buf); err != nil {
				t.Fatalf("runClusterList: %v", err)
			}
			if gotPath != tc.path {
				t.Errorf("request path = %q, want %q", gotPath, tc.path)
			}
			out := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(out, unwanted) {
					t.Errorf("summary output should not carry %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

// Every filter must reach cinder as its own query parameter — including the
// tri-state pairs, where "off" and "false" are different requests, and a count
// of zero, which is a real filter rather than an absent one.
func TestRunClusterList_Filters(t *testing.T) {
	tests := []struct {
		name   string
		flags  clusterListFlags
		want   map[string]string
		absent []string
	}{
		{
			name:   "no filters",
			want:   map[string]string{},
			absent: []string{"name", "binary", "is_up", "disabled", "num_hosts", "num_down_hosts"},
		},
		{
			name:  "--down is is_up=false, not an absent filter",
			flags: clusterListFlags{isUp: ptr(false)},
			want:  map[string]string{"is_up": "false"},
		},
		{
			name:  "--enabled is disabled=false",
			flags: clusterListFlags{isDisabled: ptr(false)},
			want:  map[string]string{"disabled": "false"},
		},
		{
			name:  "--num-down-hosts 0 is sent",
			flags: clusterListFlags{numDownHosts: ptr(0)},
			want:  map[string]string{"num_down_hosts": "0"},
		},
		{
			name:  "name and binary",
			flags: clusterListFlags{name: "cluster_01", binary: "cinder-volume"},
			want:  map[string]string{"name": "cluster_01", "binary": "cinder-volume"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var query url.Values
			fakeServer.Mux.HandleFunc("/clusters", func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(clusterListBody))
			})

			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatTable}
			flags := tc.flags
			if err := runClusterList(context.Background(), volumeClient(fakeServer, "latest"), o, &flags, &buf); err != nil {
				t.Fatalf("runClusterList: %v", err)
			}
			for key, want := range tc.want {
				if got := query.Get(key); got != want {
					t.Errorf("query %s = %q, want %q (full query %v)", key, got, want, query)
				}
			}
			for _, key := range tc.absent {
				if query.Has(key) {
					t.Errorf("query should not carry %s: %v", key, query)
				}
			}
		})
	}
}

// Cinder gated the whole /clusters resource behind 3.7; below it the endpoint
// 404s, which is indistinguishable from "no such cluster" unless koc says so
// first. No verb may issue a request below the gate.
func TestRunCluster_MicroversionGate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s below microversion 3.7", r.Method, r.URL.Path)
	})
	client := volumeClient(fakeServer, "3.6")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer

	verbs := map[string]func() error{
		"list": func() error {
			return runClusterList(context.Background(), client, o, &clusterListFlags{}, &buf)
		},
		"show": func() error {
			return runClusterShow(context.Background(), client, o, "cluster_01", "", &buf)
		},
		"set": func() error {
			return runClusterSet(context.Background(), client, o, "cluster_01", &clusterSetFlags{disable: true}, &buf)
		},
	}
	for name, run := range verbs {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatal("expected a microversion error at 3.6, got nil")
			}
			if !strings.Contains(err.Error(), "3.7") {
				t.Errorf("error should name microversion 3.7, got: %v", err)
			}
		})
	}
}

func TestRunClusterShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath, gotBinary string
	fakeServer.Mux.HandleFunc("/clusters/cluster_01@rbd-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		gotPath, gotBinary = r.URL.Path, r.URL.Query().Get("binary")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(clusterShowBody))
	})

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	err := runClusterShow(context.Background(), volumeClient(fakeServer, "latest"), o, "cluster_01@rbd-1", "cinder-volume", &buf)
	if err != nil {
		t.Fatalf("runClusterShow: %v", err)
	}
	if gotPath != "/clusters/cluster_01@rbd-1" {
		t.Errorf("request path = %q", gotPath)
	}
	// Upstream declares --binary on this verb and then drops it; cinder accepts
	// it, so koc must actually send it.
	if gotBinary != "cinder-volume" {
		t.Errorf("binary query = %q, want cinder-volume", gotBinary)
	}
	out := buf.String()
	for _, want := range []string{
		"Name", "Binary", "State", "Status", "Disabled Reason", "Hosts", "Down Hosts",
		"Last Heartbeat", "Replication Status", "Frozen", "Active Backend ID",
		"backend drained", "rbd-1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// Enable and disable are two endpoints, not one body flag, and the reason rides
// only on the disable one.
func TestRunClusterSet(t *testing.T) {
	tests := []struct {
		name     string
		flags    clusterSetFlags
		path     string
		wantBody map[string]any
	}{
		{
			name:     "disable with reason",
			flags:    clusterSetFlags{disable: true, disableReason: "backend drained", binary: "cinder-volume"},
			path:     "/clusters/disable",
			wantBody: map[string]any{"name": "cluster_01@rbd-1", "binary": "cinder-volume", "disabled_reason": "backend drained"},
		},
		{
			name:     "enable",
			flags:    clusterSetFlags{enable: true, binary: "cinder-volume"},
			path:     "/clusters/enable",
			wantBody: map[string]any{"name": "cluster_01@rbd-1", "binary": "cinder-volume"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod string
			var gotBody map[string]any
			fakeServer.Mux.HandleFunc(tc.path, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(clusterShowBody))
			})

			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatTable}
			flags := tc.flags
			if err := runClusterSet(context.Background(), volumeClient(fakeServer, "latest"), o, "cluster_01@rbd-1", &flags, &buf); err != nil {
				t.Fatalf("runClusterSet: %v", err)
			}
			if gotMethod != http.MethodPut {
				t.Errorf("method = %q, want PUT", gotMethod)
			}
			if len(gotBody) != len(tc.wantBody) {
				t.Errorf("body = %#v, want %#v", gotBody, tc.wantBody)
			}
			for k, want := range tc.wantBody {
				if gotBody[k] != want {
					t.Errorf("body[%s] = %v, want %v", k, gotBody[k], want)
				}
			}
			// The updated cluster is the command's own confirmation.
			if out := buf.String(); !strings.Contains(out, "cluster_01@rbd-1") {
				t.Errorf("output should render the updated cluster:\n%s", out)
			}
		})
	}
}
