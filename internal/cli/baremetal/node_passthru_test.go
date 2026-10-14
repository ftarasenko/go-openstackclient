package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestNodePassthru(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1/vendor_passthru/methods": {http.StatusOK, map[string]any{
			"send_raw":  map[string]any{"http_methods": []string{"POST"}, "async": true, "description": "raw IPMI", "attach": false},
			"bmc_reset": map[string]any{"http_methods": []string{"POST"}, "async": true, "description": "reset BMC", "attach": false},
		}},
		"POST /nodes/bm-1/vendor_passthru/send_raw": {http.StatusAccepted, nil},
		"GET /nodes/bm-1/vendor_passthru/status":    {http.StatusOK, map[string]any{"ok": true}},
	})
	ctx := context.Background()

	var buf bytes.Buffer
	if err := runNodePassthruList(ctx, client, &output.Options{Format: output.FormatValue}, "bm-1", &buf); err != nil {
		t.Fatal(err)
	}
	if want := "bmc_reset POST true reset BMC false\nsend_raw POST true raw IPMI false\n"; buf.String() != want {
		t.Errorf("passthru list = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	if err := runNodePassthruCall(ctx, client, "bm-1", "send_raw", "POST", []string{"raw_bytes=0x00 0x01"}, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "passthru body", fake.call("POST /nodes/bm-1/vendor_passthru/send_raw").Body, `{"raw_bytes":"0x00 0x01"}`)
	if buf.Len() != 0 {
		t.Errorf("an empty 202 printed %q", buf.String())
	}

	if err := runNodePassthruCall(ctx, client, "bm-1", "status", "GET", nil, &buf); err != nil {
		t.Fatal(err)
	}
	if fake.call("GET /nodes/bm-1/vendor_passthru/status").Body != nil {
		t.Error("GET passthru sent a body")
	}
	if strings.TrimSpace(buf.String()) != "{\n  \"ok\": true\n}" {
		t.Errorf("output = %q", buf.String())
	}
	if err := runNodePassthruCall(ctx, client, "bm-1", "x", "HEAD", nil, &buf); err == nil {
		t.Error("--http-method HEAD accepted")
	}
}
