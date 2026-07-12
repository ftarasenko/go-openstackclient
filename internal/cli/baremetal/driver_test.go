package baremetal

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

const driverListBody = `{
  "drivers": [
    {
      "name": "ipmi",
      "hosts": ["conductor-a", "conductor-b"],
      "type": "dynamic",
      "default_deploy_interface": "iscsi",
      "default_boot_interface": "pxe"
    },
    {
      "name": "redfish",
      "hosts": ["conductor-a"],
      "type": "dynamic",
      "default_deploy_interface": "direct",
      "default_boot_interface": "redfish-virtual-media"
    }
  ]
}`

func TestRunDriverList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	fakeServer.Mux.HandleFunc("/drivers", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(driverListBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runDriverList(context.Background(), client, o, &driverListFlags{}, &buf); err != nil {
		t.Fatalf("runDriverList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}

	out := buf.String()
	for _, want := range []string{
		"Supported driver(s)", "Active host(s)",
		"ipmi", "redfish", "conductor-a",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("driver table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should not appear by default.
	if strings.Contains(out, "Default Deploy Interface") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunDriverList_LongAndTypeFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/drivers", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"detail": "true",
			"type":   "dynamic",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(driverListBody))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	f := &driverListFlags{long: true, typ: "dynamic"}

	var buf bytes.Buffer
	if err := runDriverList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runDriverList returned error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"Type", "Default Deploy Interface", "Default Boot Interface", "iscsi", "pxe"} {
		if !strings.Contains(out, want) {
			t.Errorf("--long driver output missing %q\n---\n%s", want, out)
		}
	}
}
