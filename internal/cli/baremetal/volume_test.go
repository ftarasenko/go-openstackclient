package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	testConnectorUUID = "55555555-0000-0000-0000-000000000001"
	testTargetUUID    = "66666666-0000-0000-0000-000000000001"
	testVolumeID      = "44444444-0000-0000-0000-000000000001"
)

func testConnector() map[string]any {
	return map[string]any{
		"uuid": testConnectorUUID, "node_uuid": testNodeUUID, "type": "iqn",
		"connector_id": "iqn.2010-10.org.openstack:node-a", "extra": map[string]any{}, "links": []any{},
	}
}

func testTarget() map[string]any {
	return map[string]any{
		"uuid": testTargetUUID, "node_uuid": testNodeUUID, "volume_type": "iscsi", "boot_index": 0,
		"volume_id": testVolumeID, "properties": map[string]any{"target_lun": "0"}, "extra": map[string]any{},
		"links": []any{},
	}
}

func TestRunVolumeList_ShortAndLong(t *testing.T) {
	for _, tc := range []struct {
		r     volumeResource
		body  map[string]any
		long  bool
		query string
		cols  []string
	}{
		{volumeConnector, testConnector(), false, "node=bm-1", []string{"UUID", "Node UUID", "Type", "Connector ID"}},
		{volumeConnector, testConnector(), true, "detail=true&node=bm-1", []string{"Extra", "Created At"}},
		{volumeTarget, testTarget(), false, "node=bm-1", []string{"Driver Volume Type", "Boot Index", "Volume ID"}},
		{volumeTarget, testTarget(), true, "detail=true&node=bm-1", []string{"Properties", "Extra"}},
	} {
		client, fake := newFakeIronic(t, map[string]fakeRoute{
			"GET /" + tc.r.path: {http.StatusOK, map[string]any{tc.r.key: []any{tc.body}}},
		})
		o := &output.Options{Format: output.FormatJSON}
		var buf bytes.Buffer
		if err := runVolumeList(context.Background(), client, o, tc.r, &volumeListFlags{long: tc.long, node: "bm-1"}, &buf); err != nil {
			t.Fatal(err)
		}
		if q := fake.call("GET /" + tc.r.path).Query; q != tc.query {
			t.Errorf("%s long=%v: query = %q, want %q", tc.r.kind, tc.long, q, tc.query)
		}
		rows := decodeJSON[[]map[string]any](t, buf.String())
		for _, col := range tc.cols {
			if _, ok := rows[0][col]; !ok {
				t.Errorf("%s long=%v: no %q column in %v", tc.r.kind, tc.long, col, rows[0])
			}
		}
	}
}

func TestRunVolumeShow(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"GET /volume/targets/" + testTargetUUID: {http.StatusOK, testTarget()},
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runVolumeShow(context.Background(), client, o, volumeTarget, testTargetUUID, &buf); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[map[string]any](t, buf.String())
	if got["volume_id"] != testVolumeID || got["volume_type"] != "iscsi" {
		t.Errorf("show = %v", got)
	}
	if _, ok := got["links"]; ok {
		t.Error("show renders links")
	}
}

func TestRunVolumeConnectorCreate_ResolvesNodeAndSendsBody(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1":         {http.StatusOK, map[string]any{"uuid": testNodeUUID}},
		"POST /volume/connectors": {http.StatusCreated, testConnector()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	var buf bytes.Buffer
	f := &volumeConnectorFlags{node: "bm-1", connType: "iqn", connectorID: "iqn.2010-10.org.openstack:node-a", extra: []string{"rack=r1"}}
	if err := runVolumeConnectorCreate(context.Background(), client, o, f, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "create body", fake.call("POST /volume/connectors").Body, `{
		"node_uuid":"`+testNodeUUID+`","type":"iqn","connector_id":"iqn.2010-10.org.openstack:node-a",
		"extra":{"rack":"r1"}}`)
	if buf.String() != testConnectorUUID+"\n" {
		t.Errorf("output = %q", buf.String())
	}

	err := runVolumeConnectorCreate(context.Background(), client, o, &volumeConnectorFlags{node: "bm-1", connType: "nvme"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "--type") {
		t.Errorf("bad --type: err = %v", err)
	}
}

func TestRunVolumeTargetCreate_SendsBody(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/" + testNodeUUID: {http.StatusOK, map[string]any{"uuid": testNodeUUID}},
		"POST /volume/targets":       {http.StatusCreated, testTarget()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	f := &volumeTargetFlags{
		node: testNodeUUID, volumeType: "iscsi", bootIndex: 0, volumeID: testVolumeID, properties: []string{"target_lun=0"},
	}
	if err := runVolumeTargetCreate(context.Background(), client, o, f, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "create body", fake.call("POST /volume/targets").Body, `{
		"node_uuid":"`+testNodeUUID+`","volume_type":"iscsi","boot_index":0,"volume_id":"`+testVolumeID+`",
		"properties":{"target_lun":"0"}}`)
}

func TestRunVolumeSetUnset_PatchBodies(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /volume/connectors/" + testConnectorUUID: {http.StatusOK, testConnector()},
		"PATCH /volume/targets/" + testTargetUUID:       {http.StatusOK, testTarget()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	ctx := context.Background()

	cf := &volumeConnectorFlags{connType: "wwpn", connectorID: "50:00:00:00:00:00:00:01", extra: []string{"a=b"}}
	if err := runVolumeConnectorSet(ctx, client, o, testConnectorUUID, cf, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "connector set", fake.call("PATCH /volume/connectors/"+testConnectorUUID).Body, `[
		{"op":"add","path":"/type","value":"wwpn"},
		{"op":"add","path":"/connector_id","value":"50:00:00:00:00:00:00:01"},
		{"op":"add","path":"/extra/a","value":"b"}]`)

	tf := &volumeTargetFlags{bootIndex: 1, bootIndexSet: true, volumeType: "fibre_channel", properties: []string{"lun=1"}}
	if err := runVolumeTargetSet(ctx, client, o, testTargetUUID, tf, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "target set", fake.call("PATCH /volume/targets/"+testTargetUUID).Body, `[
		{"op":"add","path":"/volume_type","value":"fibre_channel"},
		{"op":"add","path":"/boot_index","value":1},
		{"op":"add","path":"/properties/lun","value":"1"}]`)

	fake.calls = nil
	if err := runVolumeUnset(ctx, client, o, volumeTarget, testTargetUUID, &volumeUnsetFlags{extra: []string{"x"}, properties: []string{"lun"}}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "target unset", fake.call("PATCH /volume/targets/"+testTargetUUID).Body, `[
		{"op":"remove","path":"/extra/x"},{"op":"remove","path":"/properties/lun"}]`)

	if err := runVolumeUnset(ctx, client, o, volumeConnector, testConnectorUUID, &volumeUnsetFlags{}, &bytes.Buffer{}); err == nil {
		t.Error("unset with no flags should fail")
	}
}

func TestRunVolumeDelete_Each(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"DELETE /volume/connectors/c1": {http.StatusNoContent, nil},
		"DELETE /volume/connectors/c2": {http.StatusNotFound, map[string]any{"error_message": "not found"}},
	})
	var buf bytes.Buffer
	err := runVolumeDelete(context.Background(), client, volumeConnector, []string{"c1", "c2"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "c2") {
		t.Errorf("err = %v", err)
	}
	if buf.String() != "Deleted volume connector c1\n" {
		t.Errorf("output = %q", buf.String())
	}
	fake.call("DELETE /volume/connectors/c1")
}
