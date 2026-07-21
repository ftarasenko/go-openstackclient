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

const eventListBody = `{
  "instanceActions": [
    {
      "action": "reboot",
      "instance_uuid": "11111111-1111-1111-1111-111111111111",
      "message": null,
      "project_id": "proj-1",
      "request_id": "req-aaaa",
      "start_time": "2016-03-04T06:27:59.000000",
      "updated_at": "2016-03-04T06:28:59.000000",
      "user_id": "user-1"
    },
    {
      "action": "create",
      "instance_uuid": "11111111-1111-1111-1111-111111111111",
      "message": null,
      "project_id": "proj-1",
      "request_id": "req-bbbb",
      "start_time": "2016-03-04T06:00:00.000000",
      "updated_at": "2016-03-04T06:01:00.000000",
      "user_id": "user-1"
    }
  ]
}`

// TestRunServerEventList covers the raw os-instance-actions GET, the
// changes-since/limit query params, and the rendered table (default columns).
func TestRunServerEventList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-instance-actions", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(eventListBody))
	})

	client := computeClient(fakeServer, "2.58")
	o := &output.Options{Format: output.FormatTable}
	f := &eventListFlags{changesSince: "2016-03-04T00:00:00Z", limit: 5}
	var buf bytes.Buffer
	if err := runServerEventList(context.Background(), client, o, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerEventList: %v", err)
	}
	if got := gotQuery.Get("changes-since"); got != "2016-03-04T00:00:00Z" {
		t.Errorf("changes-since = %q, want 2016-03-04T00:00:00Z", got)
	}
	if got := gotQuery.Get("limit"); got != "5" {
		t.Errorf("limit = %q, want 5", got)
	}
	out := buf.String()
	for _, want := range []string{"req-aaaa", "req-bbbb", "reboot", "create", serverUUID} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRunServerEventList_LimitCap confirms --limit is a hard cap on the decoded
// result, not just a page-size hint.
func TestRunServerEventList_LimitCap(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-instance-actions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(eventListBody))
	})

	client := computeClient(fakeServer, "2.58")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerEventList(context.Background(), client, o, serverUUID, &eventListFlags{limit: 1}, &buf); err != nil {
		t.Fatalf("runServerEventList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "req-aaaa") {
		t.Errorf("output missing req-aaaa:\n%s", out)
	}
	if strings.Contains(out, "req-bbbb") {
		t.Errorf("--limit 1 must cap the result; got second row:\n%s", out)
	}
}

const eventShowBody = `{
  "instanceAction": {
    "action": "reboot",
    "instance_uuid": "11111111-1111-1111-1111-111111111111",
    "message": null,
    "project_id": "proj-1",
    "request_id": "req-aaaa",
    "start_time": "2016-03-04T06:27:59.000000",
    "updated_at": "2016-03-04T06:28:59.000000",
    "user_id": "user-1",
    "events": [
      {
        "event": "compute_reboot_instance",
        "start_time": "2016-03-04T06:27:59.000000",
        "finish_time": "2016-03-04T06:28:59.000000",
        "result": "Success",
        "traceback": null
      }
    ]
  }
}`

// TestRunServerEventShow covers the raw os-instance-actions/{request_id} GET and
// the rendered single-action fields, including the flattened events list.
func TestRunServerEventShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-instance-actions/req-aaaa", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(eventShowBody))
	})

	client := computeClient(fakeServer, "2.58")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerEventShow(context.Background(), client, o, serverUUID, "req-aaaa", &buf); err != nil {
		t.Fatalf("runServerEventShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"reboot", "req-aaaa", "proj-1", "user-1", "compute_reboot_instance", "Success"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
