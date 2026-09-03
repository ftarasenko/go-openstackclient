package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// TestReduceEventResults pins the reduction, whose only interesting case is the
// unfinished one: a step nova has not resolved yet must not be reported as a
// success.
func TestReduceEventResults(t *testing.T) {
	cases := []struct {
		name    string
		events  []actionEvent
		message string
		want    string
	}{
		{name: "all succeeded", events: []actionEvent{{Result: "Success"}, {Result: "Success"}}, want: "Success"},
		{name: "one failed", events: []actionEvent{{Result: "Success"}, {Result: "Error"}}, want: "Error"},
		{name: "still running", events: []actionEvent{{Result: "Success"}, {Result: ""}}, want: "In Progress"},
		{name: "failure wins over a pending step", events: []actionEvent{{Result: ""}, {Result: "Error"}}, want: "Error"},
		{name: "no events but a message", message: "boom", want: "Error"},
		{name: "no events at all", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reduceEventResults(tc.events, tc.message); got != tc.want {
				t.Errorf("reduceEventResults = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunServerEventList_Result covers --result end to end: nova's action list
// has no outcome field, so each action's own events are read and reduced to one
// word. Without the flag the extra calls must not happen.
func TestRunServerEventList_Result(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	listBody := `{"instanceActions":[
	  {"request_id":"req-1","instance_uuid":"` + id + `","action":"live-migration",
	   "start_time":"2026-09-01T10:00:00.000000","updated_at":"2026-09-01T10:05:00.000000"},
	  {"request_id":"req-2","instance_uuid":"` + id + `","action":"reboot",
	   "start_time":"2026-09-01T09:00:00.000000","message":"Error"}
	]}`

	for _, tc := range []struct {
		name      string
		result    bool
		wantCalls int
		want      []string
		absent    []string
	}{
		{
			name: "default listing", wantCalls: 0,
			want:   []string{"req-1", "req-2", "live-migration"},
			absent: []string{"Result"},
		},
		{
			name: "with --result", result: true, wantCalls: 2,
			want: []string{"Result", "Error", "Success"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/servers/"+id+"/os-instance-actions",
				func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(listBody))
				})
			var detailCalls int
			fakeServer.Mux.HandleFunc("/servers/"+id+"/os-instance-actions/req-1",
				func(w http.ResponseWriter, _ *http.Request) {
					detailCalls++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"instanceAction":{"request_id":"req-1",
					  "events":[{"event":"conductor_live_migrate_instance","result":"Success"},
					            {"event":"compute_live_migration","result":"Success"}]}}`))
				})
			fakeServer.Mux.HandleFunc("/servers/"+id+"/os-instance-actions/req-2",
				func(w http.ResponseWriter, _ *http.Request) {
					detailCalls++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"instanceAction":{"request_id":"req-2",
					  "events":[{"event":"compute_reboot_instance","result":"Error"}]}}`))
				})

			o := &output.Options{Format: output.FormatCSV}
			var buf bytes.Buffer
			f := &eventListFlags{result: tc.result}
			if err := runServerEventList(context.Background(), computeClient(fakeServer, "2.93"), o, id, f, &buf); err != nil {
				t.Fatalf("runServerEventList: %v", err)
			}
			out := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("event list output missing %q\n---\n%s", want, out)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("event list output unexpectedly has %q\n---\n%s", absent, out)
				}
			}
			if detailCalls != tc.wantCalls {
				t.Errorf("per-action detail calls = %d, want %d", detailCalls, tc.wantCalls)
			}
		})
	}
}

// A detail call that fails leaves that action's Result blank; the listing is
// already complete without it, so it must not fail the command.
func TestRunServerEventList_ResultTolerantOfDetailFailure(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+id+"/os-instance-actions",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"instanceActions":[{"request_id":"req-1","instance_uuid":"` + id +
				`","action":"reboot","start_time":"2026-09-01T09:00:00.000000"}]}`))
		})
	fakeServer.Mux.HandleFunc("/servers/"+id+"/os-instance-actions/req-1",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runServerEventList(context.Background(), computeClient(fakeServer, "2.93"), o, id,
		&eventListFlags{result: true}, &buf); err != nil {
		t.Fatalf("runServerEventList: %v", err)
	}
	if !strings.Contains(buf.String(), "req-1") {
		t.Errorf("listing lost its row when the detail call failed\n---\n%s", buf.String())
	}
}
