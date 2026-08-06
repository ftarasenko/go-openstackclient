package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunNodeSet_HardwareInterfacesAndAutomatedClean(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/node-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		// The patch order must be deterministic — the interface families are walked
		// as a fixed slice, not a map — so an exact body comparison is meaningful.
		th.TestJSONRequest(t, r, `[
          {"op": "replace", "path": "/boot_interface", "value": "redfish-virtual-media"},
          {"op": "replace", "path": "/deploy_interface", "value": "direct"},
          {"op": "replace", "path": "/management_interface", "value": "redfish"},
          {"op": "replace", "path": "/power_interface", "value": "redfish"},
          {"op": "replace", "path": "/automated_clean", "value": true}
        ]`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid": "node-1", "name": "n1", "provision_state": "manageable"}`))
	})

	f := &nodeSetFlags{automatedClean: true, interfaces: map[string]*string{}}
	for _, name := range hardwareInterfaces {
		f.interfaces[name] = new(string)
	}
	*f.interfaces["boot"] = "redfish-virtual-media"
	*f.interfaces["deploy"] = "direct"
	*f.interfaces["management"] = "redfish"
	*f.interfaces["power"] = "redfish"

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runNodeSet(context.Background(), baremetalClient(fakeServer, "latest"), o, "node-1", f, &buf); err != nil {
		t.Fatalf("runNodeSet error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
}

// automated_clean is a tri-state: --no-automated-clean must send an explicit
// false, not omit the key (which would leave the conductor default in place).
func TestRunNodeSet_NoAutomatedCleanSendsFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes/node-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `[{"op": "replace", "path": "/automated_clean", "value": false}]`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid": "node-1"}`))
	})

	f := &nodeSetFlags{noAutomatedClean: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runNodeSet(context.Background(), baremetalClient(fakeServer, "latest"), o, "node-1", f, &buf); err != nil {
		t.Fatalf("runNodeSet error: %v", err)
	}
}

// "node unset" restores the third state (null / conductor default) and clears a
// pinned interface back to the driver default.
func TestRunNodeUnset_InterfacesAndAutomatedClean(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes/node-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `[
          {"op": "remove", "path": "/automated_clean"},
          {"op": "remove", "path": "/boot_interface"},
          {"op": "remove", "path": "/raid_interface"}
        ]`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid": "node-1"}`))
	})

	f := &nodeUnsetFlags{automatedClean: true, interfaces: map[string]*bool{}}
	for _, name := range hardwareInterfaces {
		f.interfaces[name] = new(bool)
	}
	*f.interfaces["boot"] = true
	*f.interfaces["raid"] = true

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runNodeUnset(context.Background(), baremetalClient(fakeServer, "latest"), o, "node-1", f, &buf); err != nil {
		t.Fatalf("runNodeUnset error: %v", err)
	}
}

// Every ironic interface family must be reachable as a --<family>-interface flag
// on set, and clearable on unset.
func TestNodeSetUnset_DefineEveryInterfaceFlag(t *testing.T) {
	set := newNodeSetCommand(nil, &output.Options{})
	unset := newNodeUnsetCommand(nil, &output.Options{})
	for _, name := range hardwareInterfaces {
		flag := name + "-interface"
		if set.Flags().Lookup(flag) == nil {
			t.Errorf("node set is missing --%s", flag)
		}
		if unset.Flags().Lookup(flag) == nil {
			t.Errorf("node unset is missing --%s", flag)
		}
	}
	if len(hardwareInterfaces) != 13 {
		t.Errorf("hardwareInterfaces has %d entries, want ironic's 13 families", len(hardwareInterfaces))
	}
}

// --fields/--field are the ironic CLI's spelling of -c/--column and must feed the
// same output-layer selection rather than a parallel implementation.
func TestAddFieldsAliases_FoldIntoColumns(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "comma separated", args: []string{"--fields=uuid,name"}, want: []string{"uuid", "name"}},
		{name: "repeated", args: []string{"--fields=uuid", "--fields=name"}, want: []string{"uuid", "name"}},
		{name: "singular alias", args: []string{"--field=uuid"}, want: []string{"uuid"}},
		{name: "both spellings", args: []string{"--fields=uuid", "--field=name"}, want: []string{"uuid", "name"}},
		{name: "neither", args: nil, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &output.Options{Format: output.FormatTable}
			cmd := newNodeListCommand(nil, o)
			// Stop before RunE, which would need credentials; PreRunE is where the
			// folding happens.
			cmd.RunE = func(_ *cobra.Command, _ []string) error { return nil }
			cmd.SetArgs(tc.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute(%v) error: %v", tc.args, err)
			}
			if strings.Join(o.Columns, ",") != strings.Join(tc.want, ",") {
				t.Errorf("o.Columns = %v, want %v", o.Columns, tc.want)
			}
		})
	}
}
