package volume

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serviceListClusterBody mirrors a clustered cinder deployment: a scheduler (no
// cluster, no backend state) and two cinder-volume services behind one cluster,
// one of which carries a disabled reason.
const serviceListClusterBody = `{
  "services": [
    {
      "binary": "cinder-scheduler",
      "host": "host-a",
      "zone": "nova",
      "status": "enabled",
      "state": "up",
      "updated_at": "2026-09-16T10:49:08.000000",
      "cluster": null,
      "backend_state": null,
      "disabled_reason": null
    },
    {
      "binary": "cinder-volume",
      "host": "host-a@rbd-1",
      "zone": "nova",
      "status": "enabled",
      "state": "up",
      "updated_at": "2026-09-16T10:49:09.000000",
      "cluster": "cluster_01@rbd-1",
      "backend_state": "up",
      "disabled_reason": null
    },
    {
      "binary": "cinder-volume",
      "host": "host-b@rbd-1",
      "zone": "nova",
      "status": "disabled",
      "state": "up",
      "updated_at": "2026-09-16T10:49:12.000000",
      "cluster": "cluster_01@rbd-1",
      "backend_state": "down",
      "disabled_reason": "maintenance window"
    }
  ]
}`

func serviceListFakeServer(t *testing.T) th.FakeServer {
	t.Helper()
	fakeServer := th.SetupHTTP()
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(serviceListClusterBody))
	})
	return fakeServer
}

// The default listing carries Cluster (cinder 3.7) and Backend State (3.49),
// exactly as upstream's does at a negotiated microversion that has them.
func TestRunServiceList_ClusterAndBackendState(t *testing.T) {
	fakeServer := serviceListFakeServer(t)
	defer fakeServer.Teardown()

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	if err := runServiceList(context.Background(), volumeClient(fakeServer, "latest"), o, &serviceListFlags{}, &buf); err != nil {
		t.Fatalf("runServiceList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Binary", "Host", "Zone", "Status", "State", "Updated At",
		"Cluster", "Backend State",
		"cinder-scheduler", "cluster_01@rbd-1", "down",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// Below the gating microversion the fields do not exist server-side, so the
// columns must not be rendered either.
func TestRunServiceList_MicroversionGatesColumns(t *testing.T) {
	tests := []struct {
		microversion string
		wantCluster  bool
		wantBackend  bool
	}{
		{"3.0", false, false},
		{"3.7", true, false},
		{"3.49", true, true},
		{"latest", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.microversion, func(t *testing.T) {
			fakeServer := serviceListFakeServer(t)
			defer fakeServer.Teardown()

			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatTable}
			err := runServiceList(context.Background(), volumeClient(fakeServer, tc.microversion), o, &serviceListFlags{}, &buf)
			if err != nil {
				t.Fatalf("runServiceList: %v", err)
			}
			out := buf.String()
			if got := strings.Contains(out, "Cluster"); got != tc.wantCluster {
				t.Errorf("Cluster column present = %v, want %v\n%s", got, tc.wantCluster, out)
			}
			if got := strings.Contains(out, "Backend State"); got != tc.wantBackend {
				t.Errorf("Backend State column present = %v, want %v\n%s", got, tc.wantBackend, out)
			}
		})
	}
}

// --long is upstream's flag for Disabled Reason; koc additionally surfaces the
// column when a service actually carries a reason, so a disabled host is not
// silent in the default listing.
func TestRunServiceList_DisabledReason(t *testing.T) {
	t.Run("shown without --long when a service carries one", func(t *testing.T) {
		fakeServer := serviceListFakeServer(t)
		defer fakeServer.Teardown()

		var buf bytes.Buffer
		o := &output.Options{Format: output.FormatTable}
		if err := runServiceList(context.Background(), volumeClient(fakeServer, "latest"), o, &serviceListFlags{}, &buf); err != nil {
			t.Fatalf("runServiceList: %v", err)
		}
		if out := buf.String(); !strings.Contains(out, "Disabled Reason") || !strings.Contains(out, "maintenance window") {
			t.Errorf("output should carry the disabled reason:\n%s", out)
		}
	})

	t.Run("stays out when nothing is disabled", func(t *testing.T) {
		fakeServer := th.SetupHTTP()
		defer fakeServer.Teardown()
		fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[{"binary":"cinder-volume","host":"host-a@rbd-1","zone":"nova","status":"enabled","state":"up","updated_at":"2026-09-16T10:49:09.000000"}]}`))
		})

		var buf bytes.Buffer
		o := &output.Options{Format: output.FormatTable}
		if err := runServiceList(context.Background(), volumeClient(fakeServer, "latest"), o, &serviceListFlags{}, &buf); err != nil {
			t.Fatalf("runServiceList: %v", err)
		}
		if out := buf.String(); strings.Contains(out, "Disabled Reason") {
			t.Errorf("output should not carry an all-blank Disabled Reason column:\n%s", out)
		}
	})

	t.Run("--long forces the column", func(t *testing.T) {
		fakeServer := th.SetupHTTP()
		defer fakeServer.Teardown()
		fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[{"binary":"cinder-volume","host":"host-a@rbd-1","zone":"nova","status":"enabled","state":"up","updated_at":"2026-09-16T10:49:09.000000"}]}`))
		})

		var buf bytes.Buffer
		o := &output.Options{Format: output.FormatTable}
		if err := runServiceList(context.Background(), volumeClient(fakeServer, "latest"), o, &serviceListFlags{long: true}, &buf); err != nil {
			t.Fatalf("runServiceList: %v", err)
		}
		if out := buf.String(); !strings.Contains(out, "Disabled Reason") {
			t.Errorf("--long output missing Disabled Reason:\n%s", out)
		}
	})
}
