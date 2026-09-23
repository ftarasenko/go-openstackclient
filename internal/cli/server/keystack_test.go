package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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
	if err := runServerAddServerGroup(context.Background(), client, serverUUID, serverGroupID, &serverAddServerGroupFlags{}, &buf); err != nil {
		t.Fatalf("runServerAddServerGroup: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	action, ok := gotBody["addServerGroup"].(map[string]any)
	if !ok {
		t.Fatalf("body missing addServerGroup object: %v", gotBody)
	}
	if action["server_group_id"] != serverGroupID {
		t.Errorf("server_group_id = %v, want %s", action["server_group_id"], serverGroupID)
	}
	if !strings.Contains(buf.String(), "Added server "+serverUUID+" to server group "+serverGroupID) {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerAddServerGroup_ResolvesName covers the name→ID lookup: nova's
// addServerGroup schema requires a UUID, so koc resolves a group name first.
func TestRunServerAddServerGroup_ResolvesName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_groups":[{"id":"` + serverGroupID + `","name":"web"},{"id":"other","name":"db"}]}`))
	})
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerAddServerGroup(context.Background(), client, serverUUID, "web", &serverAddServerGroupFlags{}, &buf); err != nil {
		t.Fatalf("runServerAddServerGroup: %v", err)
	}
	action, _ := gotBody["addServerGroup"].(map[string]any)
	if action["server_group_id"] != serverGroupID {
		t.Errorf("server_group_id = %v, want %s", action["server_group_id"], serverGroupID)
	}
	if !strings.Contains(buf.String(), "to server group web") {
		t.Errorf("output = %q", buf.String())
	}
}

// addServerGroupWaitFixture serves the action, a server that reports
// task_state=migrating on the first read and settles on the second, and the
// group with the given members.
func addServerGroupWaitFixture(t *testing.T, fakeServer th.FakeServer, settledStatus string, members []string) *int {
	t.Helper()
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	gets := 0
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		gets++
		task := `"migrating"`
		status := "ACTIVE"
		if gets > 1 {
			task, status = "null", settledStatus
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","status":"` + status + `","OS-EXT-STS:task_state":` + task + `}}`))
	})
	fakeServer.Mux.HandleFunc("/os-server-groups/"+serverGroupID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		quoted := make([]string, len(members))
		for i, m := range members {
			quoted[i] = `"` + m + `"`
		}
		_, _ = w.Write([]byte(`{"server_group":{"id":"` + serverGroupID + `","name":"web","policy":"anti-affinity","members":[` + strings.Join(quoted, ",") + `]}}`))
	})
	return &gets
}

func fastStatusPoll(t *testing.T) {
	t.Helper()
	prev := statusPollInterval
	statusPollInterval = time.Millisecond
	t.Cleanup(func() { statusPollInterval = prev })
}

// TestRunServerAddServerGroup_WaitMember covers --wait: poll through the
// policy-driven migration, then confirm membership before reporting success.
func TestRunServerAddServerGroup_WaitMember(t *testing.T) {
	fastStatusPoll(t)
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	gets := addServerGroupWaitFixture(t, fakeServer, "ACTIVE", []string{"x", serverUUID})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	f := &serverAddServerGroupFlags{wait: true, waitTimeout: time.Minute}
	if err := runServerAddServerGroup(context.Background(), client, serverUUID, serverGroupID, f, &buf); err != nil {
		t.Fatalf("runServerAddServerGroup: %v", err)
	}
	if *gets != 2 {
		t.Errorf("server GETs = %d, want 2 (migrating, then settled)", *gets)
	}
	if !strings.Contains(buf.String(), "Added server") {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerAddServerGroup_WaitDropped covers the conductor failure path:
// nova removes the server from the group, so --wait must fail even though
// task_state cleared.
func TestRunServerAddServerGroup_WaitDropped(t *testing.T) {
	fastStatusPoll(t)
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	addServerGroupWaitFixture(t, fakeServer, "ACTIVE", nil)

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	f := &serverAddServerGroupFlags{wait: true, waitTimeout: time.Minute}
	err := runServerAddServerGroup(context.Background(), client, serverUUID, serverGroupID, f, &buf)
	if err == nil || !strings.Contains(err.Error(), "is not a member") {
		t.Fatalf("err = %v, want not-a-member", err)
	}
	if buf.Len() != 0 {
		t.Errorf("output on failure = %q, want none", buf.String())
	}
}

// TestRunServerAddServerGroup_WaitError covers a migration that leaves the
// server in ERROR.
func TestRunServerAddServerGroup_WaitError(t *testing.T) {
	fastStatusPoll(t)
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	addServerGroupWaitFixture(t, fakeServer, "ERROR", []string{serverUUID})

	client := computeClient(fakeServer, "2.79")
	f := &serverAddServerGroupFlags{wait: true, waitTimeout: time.Minute}
	err := runServerAddServerGroup(context.Background(), client, serverUUID, serverGroupID, f, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ERROR") {
		t.Fatalf("err = %v, want ERROR status failure", err)
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
	if err := runServerList(context.Background(), client, o, f, "", "", &buf); err != nil {
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
	if err := runServerList(context.Background(), client, o, &serverListFlags{}, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	for _, k := range []string{"deleted", "created-since", "created-before", "deleted-since", "deleted-before",
		"changes-since", "changes-before"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("unexpected query param %q in default list: %v", k, gotQuery)
		}
	}
}

// TestRunServerList_ChangesWindow covers nova's own updated_at filters. They are
// upstream's, not KeyStack's, and select on when a server last changed rather
// than when it was created — so they are not interchangeable with
// --created-since/--created-before, and nova answers both with deleted servers
// included.
func TestRunServerList_ChangesWindow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	f := &serverListFlags{
		changesSince:  "2016-03-04T06:27:59Z",
		changesBefore: "2016-04-04T06:27:59Z",
	}
	var buf bytes.Buffer
	if err := runServerList(context.Background(), computeClient(fakeServer, "2.90"), o, f, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	for k, v := range map[string]string{
		"changes-since":  "2016-03-04T06:27:59Z",
		"changes-before": "2016-04-04T06:27:59Z",
		// Nova includes deleted servers in the window unless told otherwise, so
		// the listing would otherwise be mostly tombstones.
		"deleted": "false",
	} {
		if got := gotQuery.Get(k); got != v {
			t.Errorf("query %q = %q, want %q", k, got, v)
		}
	}
	// The KeyStack created-* params must not ride along.
	for _, k := range []string{"created-since", "created-before"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("unexpected query param %q: %v", k, gotQuery)
		}
	}
}

// --deleted asks for the tombstones outright, so it must win over the
// deleted=false the changes-* window otherwise sends.
func TestRunServerList_DeletedWinsOverChangesWindow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	f := &serverListFlags{deleted: true, changesSince: "2016-03-04T06:27:59Z"}
	var buf bytes.Buffer
	if err := runServerList(context.Background(), computeClient(fakeServer, "2.90"), o, f, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	if got := gotQuery.Get("deleted"); got != "true" {
		t.Errorf("query deleted = %q, want %q", got, "true")
	}
}
