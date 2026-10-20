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

const messageBody = `{"id": "m1", "event_id": "VOLUME_VOLUME_001_002", "resource_type": "VOLUME",
  "resource_uuid": "v1", "message_level": "ERROR", "user_message": "schedule allocate volume: Could not find any available weighted backend.",
  "request_id": "req-1", "created_at": "2026-10-20T10:00:00.000000", "guaranteed_until": "2026-11-19T10:00:00.000000"}`

// The listing follows cinder's messages_links and sends the project filter.
func TestRunMessageList_PagesAndFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "OpenStack-API-Version", "volume 3.3")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("marker") == "" {
			th.TestFormValues(t, r, map[string]string{"project_id": "p1"})
			_, _ = w.Write([]byte(`{"messages": [` + messageBody + `], "messages_links": [{"rel": "next", "href": "` +
				fakeServer.Endpoint() + `messages?project_id=p1&marker=m1"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"messages": [{"id": "m2", "user_message": "second"}]}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"ID"}}
	var buf bytes.Buffer
	if err := runMessageList(context.Background(), volumeClient(fakeServer, "3.3"), o, "p1", &messageListFlags{}, &buf); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(buf.String()); strings.Join(got, ",") != "m1,m2" {
		t.Errorf("ids = %v, want both pages", got)
	}
}

// --limit is a hard cap: one page, sent with limit, truncated.
func TestRunMessageList_LimitIsOnePage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	calls := 0
	fakeServer.Mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++
		th.TestFormValues(t, r, map[string]string{"limit": "1", "marker": "m0"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages": [` + messageBody + `, {"id": "m9"}], "messages_links": [{"rel": "next", "href": "x"}]}`))
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"ID"}}
	var buf bytes.Buffer
	if err := runMessageList(context.Background(), volumeClient(fakeServer, "latest"), o, "", &messageListFlags{limit: 1, marker: "m0"}, &buf); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.TrimSpace(buf.String()) != "m1" {
		t.Errorf("calls = %d, output %q", calls, buf.String())
	}
}

func TestRunMessageShowAndDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/messages/m1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message": ` + messageBody + `}`))
	})
	client := volumeClient(fakeServer, "3.70")
	var buf bytes.Buffer
	if err := runMessageShow(context.Background(), client, &output.Options{Format: output.FormatValue, Columns: []string{"user_message"}}, "m1", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "weighted backend") {
		t.Errorf("show = %q", buf.String())
	}
	buf.Reset()
	if err := runMessageDelete(context.Background(), client, []string{"m1"}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "Deleted volume message m1\n" {
		t.Errorf("delete = %q", buf.String())
	}
}

// Below 3.3 the routes do not exist; the guard says so before any request.
func TestRunMessage_RequiresMicroversion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	client := volumeClient(fakeServer, "3.0")
	err := runMessageList(context.Background(), client, &output.Options{}, "", &messageListFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires volume API microversion 3.3") {
		t.Errorf("list err = %v", err)
	}
	if err := runMessageDelete(context.Background(), client, []string{"m1"}, &bytes.Buffer{}); err == nil {
		t.Error("delete below 3.3 should fail before a request")
	}
}
