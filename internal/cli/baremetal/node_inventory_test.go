package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const nodeInventoryBody = `{
  "inventory": {
    "hostname": "node-1.example.com",
    "bmc_address": "10.0.0.9",
    "boot": {"current_boot_mode": "uefi", "pxe_interface": "52:54:00:aa:bb:cc"},
    "cpu": {"architecture": "x86_64", "count": 64, "model_name": "AMD EPYC 7502P"},
    "memory": {"physical_mb": 262144},
    "system_vendor": {
      "manufacturer": "Supermicro",
      "product_name": "AS-1114S",
      "serial_number": "S12345"
    },
    "disks": [{"name": "/dev/sda", "size": 480103981056}, {"name": "/dev/sdb", "size": 480103981056}],
    "interfaces": [
      {"name": "eno1", "mac_address": "52:54:00:aa:bb:cc", "ipv4_address": "10.0.0.21", "has_carrier": true},
      {"name": "eno2", "mac_address": "52:54:00:aa:bb:cd", "has_carrier": false}
    ]
  },
  "plugin_data": {"macs": ["52:54:00:aa:bb:cc"]}
}`

func TestRunNodeInventoryShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	fakeServer.Mux.HandleFunc("/nodes/node-1/inventory", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeInventoryBody))
	})

	client := baremetalClient(fakeServer, "1.81")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runNodeInventoryShow(context.Background(), client, o, "node-1", &buf); err != nil {
		t.Fatalf("runNodeInventoryShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotIronicVersion != "1.81" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.81", gotIronicVersion)
	}

	out := buf.String()
	for _, want := range []string{
		"hostname", "node-1.example.com", "bmc_address", "10.0.0.9",
		"x86_64", "64", "AMD EPYC 7502P", "262144", "Supermicro", "AS-1114S",
		"S12345", "uefi", "disk_count", "interface_count",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inventory show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunNodeInventorySave_StdoutAndFile(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes/node-1/inventory", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeInventoryBody))
	})

	client := baremetalClient(fakeServer, "1.81")

	// Default: raw JSON to the writer, preserving fields koc's typed structs drop
	// (here plugin_data).
	var buf bytes.Buffer
	if err := runNodeInventorySave(context.Background(), client, "node-1", &nodeInventorySaveFlags{}, &buf); err != nil {
		t.Fatalf("runNodeInventorySave returned error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("inventory save output is not valid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := got["plugin_data"]; !ok {
		t.Errorf("saved inventory JSON dropped plugin_data:\n%s", buf.String())
	}
	if _, ok := got["inventory"]; !ok {
		t.Errorf("saved inventory JSON missing inventory:\n%s", buf.String())
	}

	// --file writes to disk and nothing to the writer.
	path := filepath.Join(t.TempDir(), "inv.json")
	var fileBuf bytes.Buffer
	f := &nodeInventorySaveFlags{file: path}
	if err := runNodeInventorySave(context.Background(), client, "node-1", f, &fileBuf); err != nil {
		t.Fatalf("runNodeInventorySave --file returned error: %v", err)
	}
	if fileBuf.Len() != 0 {
		t.Errorf("--file should not write to stdout, got %q", fileBuf.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading --file output: %v", err)
	}
	if !strings.Contains(string(data), "node-1.example.com") {
		t.Errorf("--file output missing inventory data:\n%s", data)
	}
}
