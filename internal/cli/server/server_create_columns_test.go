package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// fullServerBody is a server as nova 2.100 returns it to an admin: every
// attribute `server show` can print.
const fullServerBody = `{"server":{
  "id":"6f0c5a3e-0000-4000-8000-00000000002a","name":"web-1","status":"ACTIVE","tenant_id":"p1","user_id":"u1",
  "metadata":{"k":"v"},"hostId":"h","host_status":"UP","image":"","flavor":{"original_name":"m1.small","vcpus":1},
  "created":"2026-10-01T00:00:00Z","updated":"2026-10-01T00:00:00Z","progress":0,
  "addresses":{"net":[{"addr":"192.0.2.10","version":4}]},
  "accessIPv4":"","accessIPv6":"","links":[{"rel":"self","href":"x"}],
  "OS-DCF:diskConfig":"MANUAL","OS-EXT-AZ:availability_zone":"nova",
  "config_drive":"","key_name":null,"description":null,"locked":false,"locked_reason":null,
  "OS-SRV-USG:launched_at":"2026-10-01T00:00:00Z","OS-SRV-USG:terminated_at":null,
  "OS-EXT-SRV-ATTR:host":"cmp-1","OS-EXT-SRV-ATTR:hypervisor_hostname":"cmp-1.example.com",
  "OS-EXT-SRV-ATTR:instance_name":"instance-0000002a","OS-EXT-SRV-ATTR:hostname":"web-1",
  "OS-EXT-SRV-ATTR:reservation_id":"r-1","OS-EXT-SRV-ATTR:launch_index":0,
  "OS-EXT-SRV-ATTR:kernel_id":"","OS-EXT-SRV-ATTR:ramdisk_id":"",
  "OS-EXT-SRV-ATTR:root_device_name":"/dev/vda","OS-EXT-SRV-ATTR:user_data":null,
  "OS-EXT-STS:task_state":null,"OS-EXT-STS:vm_state":"active","OS-EXT-STS:power_state":1,
  "os-extended-volumes:volumes_attached":[{"id":"vol-1","delete_on_termination":true}],
  "security_groups":[{"name":"default"}],"tags":[],"trusted_image_certificates":null,
  "server_groups":[],"pinned_availability_zone":null,"scheduler_hints":{}
}}`

// createServerMock answers a create with fullServerBody (or body, when set)
// and counts every request it sees.
func createServerMock(t *testing.T, body string) (th.FakeServer, *int) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	if body == "" {
		body = fullServerBody
	}
	var calls int
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"6f0c5a3e-0000-4000-8000-00000000002a","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/6f0c5a3e-0000-4000-8000-00000000002a", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return fakeServer, &calls
}

// An unknown -c must fail before any request: once nova has the create the
// server exists, and an error then reads as a failed create that a script
// retries into a duplicate.
func TestRunServerCreate_UnknownColumnSendsNothing(t *testing.T) {
	fakeServer, calls := createServerMock(t, "")
	o := &output.Options{Format: output.FormatValue, Columns: []string{"id", "bogus"}}
	f := &serverCreateFlags{flavor: "m1.small", image: "img"}
	err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "web-1", f, io.Discard, io.Discard)

	var ce *output.ColumnError
	if !errors.As(err, &ce) || ce.Rendering || len(ce.Unknown) != 1 || ce.Unknown[0] != "bogus" {
		t.Fatalf("err = %v, want a pre-flight ColumnError for bogus", err)
	}
	if *calls != 0 {
		t.Errorf("%d request(s) sent despite the bad column, want none", *calls)
	}
}

// The reported case: -c OS-EXT-SRV-ATTR:host on create, which `server show`
// accepts.
func TestRunServerCreate_ShowColumns(t *testing.T) {
	fakeServer, _ := createServerMock(t, "")
	o := &output.Options{Format: output.FormatValue,
		Columns: []string{"ID", "Status", "OS-EXT-SRV-ATTR:host", "Admin Password", "Networks"}}
	f := &serverCreateFlags{flavor: "m1.small", image: "img"}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "web-1", f, &buf, io.Discard); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}
	// Rows keep show's field order, as cliff's ShowOne does, not -c's.
	want := "cmp-1\nnet=192.0.2.10\npw\n6f0c5a3e-0000-4000-8000-00000000002a\nACTIVE\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// create renders what show renders, plus adminPass.
func TestRunServerCreate_ColumnsMatchShow(t *testing.T) {
	fakeServer, _ := createServerMock(t, "")
	client := computeClient(fakeServer, "2.93")

	fieldsOf := func(run func(o *output.Options, w io.Writer) error) []string {
		t.Helper()
		var buf bytes.Buffer
		if err := run(&output.Options{Format: output.FormatJSON}, &buf); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		return keys
	}
	show := fieldsOf(func(o *output.Options, w io.Writer) error {
		return runServerShow(context.Background(), client, o, "6f0c5a3e-0000-4000-8000-00000000002a", false, nil, w)
	})
	create := fieldsOf(func(o *output.Options, w io.Writer) error {
		return runServerCreate(context.Background(), client, o, "web-1",
			&serverCreateFlags{flavor: "m1.small", image: "img"}, w, io.Discard)
	})
	if len(create) != len(show)+1 || !containsFold(create, "adminPass") {
		t.Errorf("create fields %v, want show's %v plus adminPass", create, show)
	}
	for _, k := range show {
		if !containsFold(create, k) {
			t.Errorf("create is missing show's %q", k)
		}
	}
	// The catalog -c is checked against before the create must cover them all.
	for _, k := range create {
		if !containsFold(serverCreateColumns, k) {
			t.Errorf("serverCreateColumns is missing %q, so -c %s would be refused", k, k)
		}
	}
}

// A column nova leaves out — the admin-only host under a member token — renders
// empty instead of failing a create that already happened.
func TestRunServerCreate_AbsentColumnRendersEmpty(t *testing.T) {
	fakeServer, _ := createServerMock(t, `{"server":{"id":"6f0c5a3e-0000-4000-8000-00000000002a","name":"web-1","status":"BUILD"}}`)
	o := &output.Options{Format: output.FormatJSON, Columns: []string{"id", "os-ext-srv-attr:host"}}
	f := &serverCreateFlags{flavor: "m1.small", image: "img"}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "web-1", f, &buf, io.Discard); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}
	if !strings.Contains(buf.String(), `"OS-EXT-SRV-ATTR:host": null`) {
		t.Errorf("output does not carry the absent host as null:\n%s", buf.String())
	}
}
