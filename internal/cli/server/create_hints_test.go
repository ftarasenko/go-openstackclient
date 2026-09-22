package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The scalar-vs-list collapse is the whole subtlety of --hint: nova's schema
// types several hint keys as "a value *or* an array of values", and upstream
// OSC only produces an array when the key was given more than once.
func TestParseSchedulerHints(t *testing.T) {
	ok := []struct {
		name string
		in   []string
		want map[string]any
	}{
		{name: "no flags", in: nil, want: nil},
		{
			name: "a key given once stays a scalar",
			in:   []string{"group=dbf6e2b0-8f5e-4f2c-9a2f-7d1f1b2c3d4e"},
			want: map[string]any{"group": "dbf6e2b0-8f5e-4f2c-9a2f-7d1f1b2c3d4e"},
		},
		{
			name: "a repeated key becomes a list, in the order given",
			in: []string{
				"different_host=11111111-1111-4111-8111-111111111111",
				"different_host=22222222-2222-4222-8222-222222222222",
			},
			want: map[string]any{"different_host": []string{
				"11111111-1111-4111-8111-111111111111",
				"22222222-2222-4222-8222-222222222222",
			}},
		},
		{
			name: "distinct keys are independent",
			in:   []string{"same_host=33333333-3333-4333-8333-333333333333", "target_cell=cell1"},
			want: map[string]any{
				"same_host":   "33333333-3333-4333-8333-333333333333",
				"target_cell": "cell1",
			},
		},
		{
			// A --hint query= value is a JSON document; it reaches nova as the
			// string it was typed as, '=' signs and all.
			name: "the value keeps everything after the first =",
			in:   []string{`query=[">=","$free_ram_mb",1024]`, "reservation=r=1"},
			want: map[string]any{
				"query":       `[">=","$free_ram_mb",1024]`,
				"reservation": "r=1",
			},
		},
		{
			name: "an empty value is a value",
			in:   []string{"build_near_host_ip="},
			want: map[string]any{"build_near_host_ip": ""},
		},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSchedulerHints(tc.in)
			if err != nil {
				t.Fatalf("parseSchedulerHints(%v): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseSchedulerHints(%v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}

	bad := []struct {
		name string
		in   []string
	}{
		{name: "no separator", in: []string{"group"}},
		{name: "empty key", in: []string{"=value"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSchedulerHints(tc.in); err == nil {
				t.Errorf("parseSchedulerHints(%v) accepted a malformed hint", tc.in)
			}
		})
	}
}

// Hints sit beside the "server" object in the create body, not inside it, and
// nothing about them is validated locally beyond the key=value shape.
func TestRunServerCreate_HintsRideBesideTheServerObject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"db-2","status":"ACTIVE"}}`))
	})

	f := &serverCreateFlags{
		image:  "img-uuid",
		flavor: "m1.small",
		hints: []string{
			"group=dbf6e2b0-8f5e-4f2c-9a2f-7d1f1b2c3d4e",
			"different_host=11111111-1111-4111-8111-111111111111",
			"different_host=22222222-2222-4222-8222-222222222222",
			// A key nova knows nothing about: the deployment's own filter reads
			// it, so it must survive to the wire unchanged.
			"rack=b12",
		},
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "db-2", f, &buf, io.Discard); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}

	hints, ok := body["os:scheduler_hints"].(map[string]any)
	if !ok {
		t.Fatalf("create body carries no top-level os:scheduler_hints: %v", body)
	}
	if srv, _ := body["server"].(map[string]any); srv["os:scheduler_hints"] != nil ||
		srv["scheduler_hints"] != nil {
		t.Errorf("hints leaked into the server object: %v", srv)
	}
	if got := hints["group"]; got != "dbf6e2b0-8f5e-4f2c-9a2f-7d1f1b2c3d4e" {
		t.Errorf("group hint = %v, want the server-group UUID", got)
	}
	if got := hints["rack"]; got != "b12" {
		t.Errorf("unknown hint rack = %v, want b12 passed through", got)
	}
	diff, _ := hints["different_host"].([]any)
	if len(diff) != 2 || diff[0] != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("different_host = %v, want both UUIDs in order", hints["different_host"])
	}
}

// A malformed hint must fail before anything is sent.
func TestRunServerCreate_MalformedHintFailsBeforeRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	called := false
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	})

	f := &serverCreateFlags{image: "img-uuid", flavor: "m1.small", hints: []string{"group"}}
	var buf bytes.Buffer
	err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"),
		&output.Options{Format: output.FormatTable}, "db-2", f, &buf, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--hint") {
		t.Fatalf("error = %v, want it to name --hint", err)
	}
	if called {
		t.Error("a malformed --hint still issued the create request")
	}
}

// A flag registered on the command must reach the seam that puts it on the
// wire: exercised through cobra this catches a mis-bound flag, which the seam
// test cannot, because it is handed the parsed value.
func TestExec_ServerCreate_HintReachesTheRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"db-3","status":"ACTIVE"}}`))
	})

	a := &auth.Options{}
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})

	cmd := newServerCreateCommand(a, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{
		"db-3", "--flavor", "m1.small",
		"--image", "44444444-4444-4444-8444-444444444444",
		"--hint", "same_host=55555555-5555-4555-8555-555555555555",
		"--hint", "rack=b12",
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("server create --hint: %v", err)
	}

	hints, ok := body["os:scheduler_hints"].(map[string]any)
	if !ok {
		t.Fatalf("--hint did not reach the request; body was %v", body)
	}
	if hints["same_host"] != "55555555-5555-4555-8555-555555555555" || hints["rack"] != "b12" {
		t.Errorf("scheduler hints = %v, want both flag values", hints)
	}
}

// --server-group is the one hint with a name→ID lookup attached, and it is the
// authority when a bare --hint group= is also given.
func TestRunServerCreate_ServerGroupResolvesAndWins(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_groups":[{"id":"` + serverGroupID + `","name":"web","policy":"anti-affinity"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"web-9","status":"ACTIVE"}}`))
	})

	f := &serverCreateFlags{
		image:       "img-uuid",
		flavor:      "m1.small",
		serverGroup: "web",
		hints:       []string{"group=99999999-9999-4999-8999-999999999999", "rack=b12"},
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "web-9", f, &buf, io.Discard); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}

	hints, ok := body["os:scheduler_hints"].(map[string]any)
	if !ok {
		t.Fatalf("create body carries no os:scheduler_hints: %v", body)
	}
	if got := hints["group"]; got != serverGroupID {
		t.Errorf("group hint = %v, want the resolved %s from --server-group", got, serverGroupID)
	}
	// The other hints are untouched by the override.
	if got := hints["rack"]; got != "b12" {
		t.Errorf("rack hint = %v, want b12", got)
	}
}

// --server-group alone still produces the hint, with no --hint given.
func TestRunServerCreate_ServerGroupByIDNeedsNoLookup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	listed := false
	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, _ *http.Request) {
		listed = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_groups":[]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"web-9","status":"ACTIVE"}}`))
	})

	f := &serverCreateFlags{image: "img-uuid", flavor: "m1.small", serverGroup: serverGroupID}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"),
		&output.Options{Format: output.FormatTable}, "web-9", f, &buf, io.Discard); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}
	if listed {
		t.Error("--server-group <uuid> listed the groups; a UUID needs no lookup")
	}
	hints, _ := body["os:scheduler_hints"].(map[string]any)
	if hints["group"] != serverGroupID {
		t.Errorf("group hint = %v, want %s", hints["group"], serverGroupID)
	}
}
