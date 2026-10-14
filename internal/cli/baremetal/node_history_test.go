package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestNodeHistory(t *testing.T) {
	event := map[string]any{
		"uuid": "e1", "created_at": "2026-10-14T00:00:00+00:00", "severity": "ERROR", "event": "Deploy failed",
		"event_type": "deployment", "conductor": "c1", "user": "u1", "links": []any{},
	}
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1/history":    {http.StatusOK, map[string]any{"history": []any{event}}},
		"GET /nodes/bm-1/history/e1": {http.StatusOK, event},
	})
	ctx := context.Background()

	var buf bytes.Buffer
	if err := runNodeHistoryList(ctx, client, &output.Options{Format: output.FormatJSON}, "bm-1", true, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /nodes/bm-1/history").Query; q != "detail=True" {
		t.Errorf("query = %q", q)
	}
	rows := decodeJSON[[]map[string]any](t, buf.String())
	if rows[0]["Event Origin Type"] != "deployment" || rows[0]["Description of the event"] != "Deploy failed" || len(rows[0]) != 11 {
		t.Errorf("history list --long row = %v", rows[0])
	}

	buf.Reset()
	if err := runNodeHistoryGet(ctx, client, &output.Options{Format: output.FormatJSON}, "bm-1", "e1", &buf); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[map[string]any](t, buf.String())
	if got["severity"] != "ERROR" {
		t.Errorf("history get = %v", got)
	}
	if _, ok := got["links"]; ok {
		t.Error("history get renders links")
	}
}

func TestNodeChildrenList(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1/children": {http.StatusOK, map[string]any{"children": []string{"c1", "c2"}}},
		"GET /nodes/old/children":  {http.StatusNotFound, map[string]any{"error_message": ""}},
	})
	var buf bytes.Buffer
	if err := runNodeChildrenList(context.Background(), client, &output.Options{Format: output.FormatValue}, "bm-1", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "c1\nc2\n" {
		t.Errorf("output = %q", buf.String())
	}
	// The fake advertises 1.96, so a 404 is "no such node", not the version.
	err := runNodeChildrenList(context.Background(), client, &output.Options{Format: output.FormatValue}, "old", &buf)
	if err == nil || strings.Contains(err.Error(), "requires ironic API") {
		t.Errorf("err = %v", err)
	}
}
