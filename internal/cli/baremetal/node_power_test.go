package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

func TestRunNodePower_TargetsAndBody(t *testing.T) {
	cases := []struct {
		name       string
		target     nodes.TargetPowerState
		soft       bool
		wantTarget string
	}{
		{"on", nodes.PowerOn, false, "power on"},
		{"off", nodes.PowerOff, false, "power off"},
		{"soft off", nodes.PowerOff, true, "soft power off"},
		{"reboot", nodes.Rebooting, false, "rebooting"},
		{"soft reboot", nodes.Rebooting, true, "soft rebooting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod, gotIronicVersion string
			var gotBody map[string]any
			fakeServer.Mux.HandleFunc("/nodes/node-a/states/power", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &gotBody)
				w.WriteHeader(http.StatusAccepted)
			})

			client := baremetalClient(fakeServer, "1.80")

			var buf bytes.Buffer
			if err := runNodePower(context.Background(), client, "node-a", tc.target, tc.soft, &buf); err != nil {
				t.Fatalf("runNodePower returned error: %v", err)
			}

			if gotMethod != http.MethodPut {
				t.Errorf("request method = %q, want PUT", gotMethod)
			}
			if gotIronicVersion != "1.80" {
				t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
			}
			if gotBody["target"] != tc.wantTarget {
				t.Errorf("power body target = %v, want %q", gotBody["target"], tc.wantTarget)
			}
			if !strings.Contains(buf.String(), tc.wantTarget) {
				t.Errorf("output = %q, want it to mention %q", buf.String(), tc.wantTarget)
			}
		})
	}
}
