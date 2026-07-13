package server

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// TestRunServerAddServerGroup covers the KeyStack addServerGroup action
// (KCP-703): a server action posted at the negotiated microversion.
func TestRunServerAddServerGroup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerAddServerGroup(context.Background(), client, serverUUID, "grp-9", &buf); err != nil {
		t.Fatalf("runServerAddServerGroup: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	action, ok := gotBody["addServerGroup"].(map[string]any)
	if !ok {
		t.Fatalf("body missing addServerGroup object: %v", gotBody)
	}
	if action["server_group_id"] != "grp-9" {
		t.Errorf("server_group_id = %v, want grp-9", action["server_group_id"])
	}
	if !strings.Contains(buf.String(), "Added server "+serverUUID+" to server group grp-9") {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerRemoveServerGroup covers the KeyStack removeServerGroup action
// (KCP-703): the action body is {"removeServerGroup": null}.
func TestRunServerRemoveServerGroup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	present := false
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		_, present = gotBody["removeServerGroup"]
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerRemoveServerGroup(context.Background(), client, serverUUID, &buf); err != nil {
		t.Fatalf("runServerRemoveServerGroup: %v", err)
	}
	if !present {
		t.Errorf("body missing removeServerGroup key: %v", gotBody)
	}
	if !strings.Contains(buf.String(), "Removed server "+serverUUID+" from its server group") {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerSet_AvailabilityZone covers the KeyStack per-instance AZ update
// (KCP-1211): a raw server PUT carrying availability_zone.
func TestRunServerSet_AvailabilityZone(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","name":"web-1"}}`))
	})

	client := computeClient(fakeServer, "2.90")
	f := &serverSetFlags{availabilityZone: "az-2"}
	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	server, ok := gotBody["server"].(map[string]any)
	if !ok {
		t.Fatalf("body missing server object: %v", gotBody)
	}
	if server["availability_zone"] != "az-2" {
		t.Errorf("availability_zone = %v, want az-2", server["availability_zone"])
	}
}

// TestRunServerList_KeyStackFilters covers the KeyStack server-list filters
// (KCP-1768 created-/deleted-* and KCP-2417 --deleted): they are appended as
// query params only when set.
func TestRunServerList_KeyStackFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[]}`))
	})

	client := computeClient(fakeServer, "2.90")
	o := &output.Options{Format: output.FormatTable}
	f := &serverListFlags{
		deleted:       true,
		createdSince:  "2016-03-04T06:27:59Z",
		createdBefore: "2016-04-04T06:27:59Z",
		deletedSince:  "2016-05-04T06:27:59Z",
		deletedBefore: "2016-06-04T06:27:59Z",
	}
	var buf bytes.Buffer
	if err := runServerList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	want := map[string]string{
		"deleted":        "true",
		"created-since":  "2016-03-04T06:27:59Z",
		"created-before": "2016-04-04T06:27:59Z",
		"deleted-since":  "2016-05-04T06:27:59Z",
		"deleted-before": "2016-06-04T06:27:59Z",
	}
	for k, v := range want {
		if got := gotQuery.Get(k); got != v {
			t.Errorf("query %q = %q, want %q", k, got, v)
		}
	}
}

// TestRunServerList_DefaultQueryUnchanged guards the graceful-degradation
// promise: with no KeyStack flags set, no created-/deleted-* params leak into
// the request, so the default list is byte-identical to vanilla nova.
func TestRunServerList_DefaultQueryUnchanged(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[]}`))
	})

	client := computeClient(fakeServer, "2.90")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerList(context.Background(), client, o, &serverListFlags{}, &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	for _, k := range []string{"deleted", "created-since", "created-before", "deleted-since", "deleted-before"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("unexpected query param %q in default list: %v", k, gotQuery)
		}
	}
}
