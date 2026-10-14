package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunShardList(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /shards": {http.StatusOK, map[string]any{"shards": []any{
			map[string]any{"name": "rack-a", "count": 3},
			map[string]any{"name": "rack-b", "count": 1},
		}}},
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runShardList(context.Background(), client, o, &buf); err != nil {
		t.Fatal(err)
	}
	fake.call("GET /shards")
	jsonEq(t, "shards", decodeJSON[[]map[string]any](t, buf.String()),
		`[{"Name":"rack-a","Count":3},{"Name":"rack-b","Count":1}]`)
}

// Zed point releases before ironic 21.4 stop at 1.80, where /v1/shards is
// not routed.
func TestRunShardList_OldCloudNamesVersion(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /shards": {http.StatusNotFound, map[string]any{"error_message": "Not Found"}},
	})
	fake.max = "1.80"
	err := runShardList(context.Background(), client, &output.Options{Format: output.FormatTable}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires ironic API 1.82") || !strings.Contains(err.Error(), "up to 1.80") {
		t.Errorf("err = %v", err)
	}
}

func TestRunNodeSetUnset_Shard(t *testing.T) {
	node := map[string]any{"uuid": testNodeUUID, "shard": "rack-a"}
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /nodes/bm-1": {http.StatusOK, node},
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runNodeSet(context.Background(), client, o, "bm-1", &nodeSetFlags{shard: "rack-a"}, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "set patch", fake.call("PATCH /nodes/bm-1").Body, `[{"op":"replace","path":"/shard","value":"rack-a"}]`)
	if got := decodeJSON[map[string]any](t, buf.String()); got["shard"] != "rack-a" {
		t.Errorf("node set renders shard = %v", got["shard"])
	}

	fake.calls = nil
	if err := runNodeUnset(context.Background(), client, o, "bm-1", &nodeUnsetFlags{shard: true}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "unset patch", fake.call("PATCH /nodes/bm-1").Body, `[{"op":"remove","path":"/shard"}]`)
}
