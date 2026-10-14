package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestNodeConsoleAndSecureBoot(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1/states/console": {http.StatusOK, map[string]any{
			"console_enabled": true, "console_info": map[string]any{"type": "shellinabox", "url": "http://192.0.2.10:8023"},
		}},
		"PUT /nodes/bm-1/states/console":     {http.StatusAccepted, nil},
		"PUT /nodes/bm-2/states/console":     {http.StatusAccepted, nil},
		"PUT /nodes/bm-1/states/secure_boot": {http.StatusAccepted, nil},
	})
	ctx := context.Background()

	var buf bytes.Buffer
	if err := runNodeConsoleShow(ctx, client, &output.Options{Format: output.FormatJSON}, "bm-1", &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "console show", decodeJSON[any](t, buf.String()),
		`{"console_enabled":true,"console_info":{"type":"shellinabox","url":"http://192.0.2.10:8023"}}`)

	buf.Reset()
	if err := runNodeStateToggle(ctx, client, []string{"bm-1", "bm-2"}, "console", false, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "console disable body", fake.call("PUT /nodes/bm-2/states/console").Body, `{"enabled":false}`)
	if buf.String() != "Requested console off for node bm-1\nRequested console off for node bm-2\n" {
		t.Errorf("output = %q", buf.String())
	}

	if err := runNodeStateToggle(ctx, client, []string{"bm-1"}, "secure_boot", true, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "secure boot body", fake.call("PUT /nodes/bm-1/states/secure_boot").Body, `{"target":true}`)
}
