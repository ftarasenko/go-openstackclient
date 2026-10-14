package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNodeVmedia(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /nodes/bm-1/vmedia":   {http.StatusNoContent, nil},
		"DELETE /nodes/bm-1/vmedia": {http.StatusNoContent, nil},
	})
	ctx := context.Background()
	var buf bytes.Buffer
	if err := runNodeVmediaAttach(ctx, client, "bm-1", "http://192.0.2.1/boot.iso", "cdrom", "http", &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "attach body", fake.call("POST /nodes/bm-1/vmedia").Body,
		`{"device_type":"cdrom","image_url":"http://192.0.2.1/boot.iso","image_download_source":"http"}`)
	if err := runNodeVmediaDetach(ctx, client, "bm-1", []string{"cdrom", "floppy"}, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("DELETE /nodes/bm-1/vmedia").Query; q != "device_types=cdrom%2Cfloppy" {
		t.Errorf("detach query = %q", q)
	}
	if err := runNodeVmediaAttach(ctx, client, "bm-1", "x", "usb", "", &buf); err == nil {
		t.Error("--device-type usb accepted")
	}
}

// On a cloud below 1.89 the vmedia route does not exist and ironic answers
// 404; the error names the version instead.
func TestNodeVmedia_OldCloudNamesVersion(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"POST /nodes/bm-1/vmedia": {http.StatusNotFound, map[string]any{"error_message": ""}},
	})
	fakeMax := client.Microversion
	client.Microversion = "1.82"
	defer func() { client.Microversion = fakeMax }()
	err := runNodeVmediaAttach(context.Background(), client, "bm-1", "http://192.0.2.1/a.iso", "cdrom", "", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires ironic API 1.89 (OpenStack 2024.1), but --os-baremetal-api-version pins 1.82") {
		t.Errorf("err = %v", err)
	}
}
