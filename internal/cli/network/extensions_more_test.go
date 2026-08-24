package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --- network rbac -----------------------------------------------------------

func TestRunRBACList_SendsFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rbac_policies": [{"id": "` + extRBACID + `", "object_type": "network",
		  "object_id": "` + extNetworkID + `", "action": "access_as_shared",
		  "target_tenant": "99999999-9999-9999-9999-999999999999"}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	err := runRBACList(context.Background(), networkClient(fakeServer), o,
		"access_as_shared", "network", "99999999-9999-9999-9999-999999999999", &out)
	if err != nil {
		t.Fatalf("runRBACList returned error: %v", err)
	}
	if !strings.Contains(gotQuery, "action=access_as_shared") {
		t.Errorf("action filter missing from query: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "target_tenant=99999999") {
		t.Errorf("target_tenant filter missing from query: %q", gotQuery)
	}
	if !strings.Contains(out.String(), extRBACID) {
		t.Errorf("rendered output missing the policy ID:\n%s", out.String())
	}
}

func TestRunRBACList_ErrorOnNon2xx(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runRBACList(context.Background(), networkClient(fakeServer), o, "", "", "", &out); err == nil {
		t.Fatal("expected an error from a 500 response")
	}
}

func TestRunRBACShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/rbac-policies/"+extRBACID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rbac_policy": {"id": "` + extRBACID + `", "object_type": "network",
		  "object_id": "` + extNetworkID + `", "action": "access_as_external",
		  "target_tenant": "*", "project_id": "88888888-8888-8888-8888-888888888888"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRBACShow(context.Background(), networkClient(fakeServer), o, extRBACID, &out); err != nil {
		t.Fatalf("runRBACShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), "access_as_external") {
		t.Errorf("action missing from output:\n%s", out.String())
	}
}

func TestRunRBACShow_ErrorOnNotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/rbac-policies/"+extRBACID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRBACShow(context.Background(), networkClient(fakeServer), o, extRBACID, &out); err == nil {
		t.Fatal("expected an error from a 404 response")
	}
}

func TestRunRBACSet_SendsOnlyTargetProject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/rbac-policies/"+extRBACID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPut, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rbac_policy": {"id": "` + extRBACID + `", "object_type": "network",
		  "object_id": "` + extNetworkID + `", "action": "access_as_shared",
		  "target_tenant": "77777777-7777-7777-7777-777777777777"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRBACSet(context.Background(), networkClient(fakeServer), o, extRBACID,
		"77777777-7777-7777-7777-777777777777", &out)
	if err != nil {
		t.Fatalf("runRBACSet returned error: %v", err)
	}
	policy := body["rbac_policy"].(map[string]any)
	th.AssertEquals(t, "77777777-7777-7777-7777-777777777777", policy["target_tenant"])
	// The update schema has nothing else in it: no action/object_type/object_id.
	for _, k := range []string{"action", "object_type", "object_id"} {
		if _, present := policy[k]; present {
			t.Errorf("%s sent although only the target project is mutable: %#v", k, policy)
		}
	}
}

func TestRunRBACSet_ErrorOnNon2xx(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/rbac-policies/"+extRBACID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRBACSet(context.Background(), networkClient(fakeServer), o, extRBACID, "x", &out); err == nil {
		t.Fatal("expected an error from a 400 response")
	}
}

func TestRunRBACDelete_AttemptsEveryIDAndJoinsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const goodID = "cccc1111-1111-1111-1111-cccccccccccc"
	const badID = "dddd2222-2222-2222-2222-dddddddddddd"
	var deleted []string
	fakeServer.Mux.HandleFunc("/rbac-policies/"+goodID, func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, goodID)
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/rbac-policies/"+badID, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, badID)
		w.WriteHeader(http.StatusNotFound)
	})

	err := runRBACDelete(context.Background(), networkClient(fakeServer), []string{goodID, badID})
	if err == nil {
		t.Fatal("expected an error naming the failed delete")
	}
	if len(deleted) != 2 {
		t.Fatalf("expected both IDs to be attempted, got %v", deleted)
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("error does not name the failed ID %s: %v", badID, err)
	}
}

// --- network segment --------------------------------------------------------

func TestRunSegmentList_FiltersByNetworkAndType(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	handleNetworkLookup(fakeServer, "public", extNetworkID)
	var gotQuery string
	fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"segments": [{"id": "` + extSegmentID + `", "name": "seg1",
		  "network_id": "` + extNetworkID + `", "network_type": "vlan", "segmentation_id": 42}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	err := runSegmentList(context.Background(), networkClient(fakeServer), o, "public", "vlan", "physnet1", &out)
	if err != nil {
		t.Fatalf("runSegmentList returned error: %v", err)
	}
	if !strings.Contains(gotQuery, "network_type=vlan") {
		t.Errorf("network_type filter missing from query: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "network_id="+extNetworkID) {
		t.Errorf("network_id filter missing from query (network name should resolve): %q", gotQuery)
	}
	if !strings.Contains(out.String(), extSegmentID) {
		t.Errorf("rendered output missing the segment ID:\n%s", out.String())
	}
}

func TestRunSegmentList_NoNetworkFilterSkipsResolution(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "network_id=") {
			t.Errorf("network_id should not be filtered when --network was not given: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"segments": []}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runSegmentList(context.Background(), networkClient(fakeServer), o, "", "", "", &out); err != nil {
		t.Fatalf("runSegmentList returned error: %v", err)
	}
}

func TestRunSegmentList_ErrorOnMalformedBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runSegmentList(context.Background(), networkClient(fakeServer), o, "", "", "", &out); err == nil {
		t.Fatal("expected an error from a malformed response body")
	}
}

func TestRunSegmentShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"segment": {"id": "` + extSegmentID + `", "name": "seg1",
		  "network_id": "` + extNetworkID + `", "network_type": "vxlan", "segmentation_id": 55}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runSegmentShow(context.Background(), networkClient(fakeServer), o, extSegmentID, &out); err != nil {
		t.Fatalf("runSegmentShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), "vxlan") {
		t.Errorf("network_type missing from output:\n%s", out.String())
	}
}

func TestRunSegmentShow_ErrorOnNotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runSegmentShow(context.Background(), networkClient(fakeServer), o, extSegmentID, &out); err == nil {
		t.Fatal("expected an error from a 404 response")
	}
}

func TestRunSegmentCreate_ResolvesNetworkAndSendsAllFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	handleNetworkLookup(fakeServer, "public", extNetworkID)
	var body map[string]any
	fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"segment": {"id": "` + extSegmentID + `", "name": "seg1",
		  "network_id": "` + extNetworkID + `", "network_type": "vlan",
		  "physical_network": "physnet1", "segmentation_id": 200}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &segmentCreateFlags{
		network: "public", networkType: "vlan", physicalNetwork: "physnet1",
		description: "a segment", segmentationID: 200,
	}
	if err := runSegmentCreate(context.Background(), networkClient(fakeServer), o, "seg1", f, &out); err != nil {
		t.Fatalf("runSegmentCreate returned error: %v", err)
	}
	seg := body["segment"].(map[string]any)
	th.AssertEquals(t, extNetworkID, seg["network_id"])
	th.AssertEquals(t, "vlan", seg["network_type"])
	th.AssertEquals(t, "physnet1", seg["physical_network"])
	th.AssertEquals(t, float64(200), seg["segmentation_id"])
	th.AssertEquals(t, "a segment", seg["description"])
}

func TestRunSegmentCreate_ErrorOnUnresolvableNetwork(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// Two networks named "dup" makes the resolver ambiguous.
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks": [
		  {"id": "11110000-0000-0000-0000-000000000001", "name": "dup"},
		  {"id": "22220000-0000-0000-0000-000000000002", "name": "dup"}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &segmentCreateFlags{network: "dup", networkType: "vlan"}
	if err := runSegmentCreate(context.Background(), networkClient(fakeServer), o, "seg1", f, &out); err == nil {
		t.Fatal("expected an ambiguous-name error")
	}
}

func TestRunSegmentSet_AllFieldsChanged(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPut, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"segment": {"id": "` + extSegmentID + `", "name": "renamed",
		  "description": "new desc", "network_id": "` + extNetworkID + `",
		  "network_type": "vlan", "segmentation_id": 7}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &segmentSetFlags{
		name: "renamed", description: "new desc", segmentationID: 7,
		nameSet: true, descSet: true, segSet: true,
	}
	if err := runSegmentSet(context.Background(), networkClient(fakeServer), o, extSegmentID, f, &out); err != nil {
		t.Fatalf("runSegmentSet returned error: %v", err)
	}
	seg := body["segment"].(map[string]any)
	th.AssertEquals(t, "renamed", seg["name"])
	th.AssertEquals(t, "new desc", seg["description"])
	th.AssertEquals(t, float64(7), seg["segmentation_id"])
}

func TestRunSegmentSet_ErrorOnNon2xx(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &segmentSetFlags{name: "x", nameSet: true}
	if err := runSegmentSet(context.Background(), networkClient(fakeServer), o, extSegmentID, f, &out); err == nil {
		t.Fatal("expected an error from a 400 response")
	}
}

func TestRunSegmentDelete_AttemptsEveryIDAndJoinsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const goodID = "eeee3333-3333-3333-3333-eeeeeeeeeeee"
	const badID = "ffff4444-4444-4444-4444-ffffffffffff"
	var deleted []string
	fakeServer.Mux.HandleFunc("/segments/"+goodID, func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, goodID)
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/segments/"+badID, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, badID)
		w.WriteHeader(http.StatusInternalServerError)
	})

	err := runSegmentDelete(context.Background(), networkClient(fakeServer), []string{goodID, badID})
	if err == nil {
		t.Fatal("expected an error naming the failed delete")
	}
	if len(deleted) != 2 {
		t.Fatalf("expected both IDs to be attempted, got %v", deleted)
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("error does not name the failed ID %s: %v", badID, err)
	}
}

// --- floating ip port forwarding --------------------------------------------

func TestRunPortForwardingList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"port_forwardings": [{"id": "` + extPFID + `", "protocol": "tcp",
		  "external_port": 2222, "internal_port": 22, "internal_ip_address": "192.0.2.10",
		  "internal_port_id": "` + extPortID + `"}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runPortForwardingList(context.Background(), networkClient(fakeServer), o, extFIPID, &out); err != nil {
		t.Fatalf("runPortForwardingList returned error: %v", err)
	}
	if !strings.Contains(out.String(), extPFID) || !strings.Contains(out.String(), "2222") {
		t.Errorf("rendered output missing expected fields:\n%s", out.String())
	}
}

func TestRunPortForwardingList_ErrorOnNon2xx(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runPortForwardingList(context.Background(), networkClient(fakeServer), o, extFIPID, &out); err == nil {
		t.Fatal("expected an error from a 403 response")
	}
}

func TestRunPortForwardingShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+extPFID,
		func(w http.ResponseWriter, r *http.Request) {
			th.AssertEquals(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"port_forwarding": {"id": "` + extPFID + `", "protocol": "udp",
			  "internal_port_id": "` + extPortID + `", "internal_ip_address": "192.0.2.11",
			  "internal_port": 53, "external_port": 5353, "description": "dns"}}`))
		})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runPortForwardingShow(context.Background(), networkClient(fakeServer), o, extFIPID, extPFID, &out); err != nil {
		t.Fatalf("runPortForwardingShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), "udp") || !strings.Contains(out.String(), "dns") {
		t.Errorf("rendered output missing expected fields:\n%s", out.String())
	}
}

func TestRunPortForwardingShow_ErrorOnNotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+extPFID,
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runPortForwardingShow(context.Background(), networkClient(fakeServer), o, extFIPID, extPFID, &out); err == nil {
		t.Fatal("expected an error from a 404 response")
	}
}

func TestRunPortForwardingSet_ResolvesPortAndSendsRangesWhenGiven(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": [{"id": "` + extPortID + `", "name": "vm-port"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+extPFID,
		func(w http.ResponseWriter, r *http.Request) {
			th.AssertEquals(t, http.MethodPut, r.Method)
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"port_forwarding": {"id": "` + extPFID + `", "protocol": "tcp",
			  "internal_port_id": "` + extPortID + `", "internal_port_range": "22:23",
			  "external_port_range": "2222:2223", "description": "updated"}}`))
		})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &portForwardingFlags{
		port: "vm-port", internalPortRange: "22:23", externalPortRange: "2222:2223",
		protocol: "tcp", description: "updated", descSet: true,
	}
	err := runPortForwardingSet(context.Background(), networkClient(fakeServer), o, extFIPID, extPFID, f, &out)
	if err != nil {
		t.Fatalf("runPortForwardingSet returned error: %v", err)
	}
	pf := body["port_forwarding"].(map[string]any)
	th.AssertEquals(t, extPortID, pf["internal_port_id"])
	th.AssertEquals(t, "22:23", pf["internal_port_range"])
	th.AssertEquals(t, "2222:2223", pf["external_port_range"])
	th.AssertEquals(t, "updated", pf["description"])
}

func TestRunPortForwardingSet_LeavesDescriptionAndPortUntouchedWhenNotGiven(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+extPFID,
		func(w http.ResponseWriter, r *http.Request) {
			th.AssertEquals(t, http.MethodPut, r.Method)
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"port_forwarding": {"id": "` + extPFID + `", "protocol": "tcp",
			  "internal_port_id": "` + extPortID + `"}}`))
		})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	// No --port and no --description: neither internal_port_id nor description
	// should be sent, leaving the stored values alone.
	f := &portForwardingFlags{protocol: "tcp"}
	err := runPortForwardingSet(context.Background(), networkClient(fakeServer), o, extFIPID, extPFID, f, &out)
	if err != nil {
		t.Fatalf("runPortForwardingSet returned error: %v", err)
	}
	pf := body["port_forwarding"].(map[string]any)
	for _, k := range []string{"internal_port_id", "description"} {
		if _, present := pf[k]; present {
			t.Errorf("%s sent although its flag was not given: %#v", k, pf)
		}
	}
}

func TestRunPortForwardingSet_ErrorOnUnresolvablePort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &portForwardingFlags{port: "vm-port"}
	err := runPortForwardingSet(context.Background(), networkClient(fakeServer), o, extFIPID, extPFID, f, &out)
	if err == nil {
		t.Fatal("expected an error when the port resolver's list call fails")
	}
}

func TestRunPortForwardingDelete_AttemptsEveryIDAndJoinsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const goodID = "aaaa1111-1111-1111-1111-aaaaaaaaaaaa"
	const badID = "bbbb2222-2222-2222-2222-bbbbbbbbbbbb"
	var deleted []string
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+goodID,
		func(w http.ResponseWriter, r *http.Request) {
			deleted = append(deleted, goodID)
			th.AssertEquals(t, http.MethodDelete, r.Method)
			w.WriteHeader(http.StatusNoContent)
		})
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+badID,
		func(w http.ResponseWriter, _ *http.Request) {
			deleted = append(deleted, badID)
			w.WriteHeader(http.StatusNotFound)
		})

	err := runPortForwardingDelete(context.Background(), networkClient(fakeServer), extFIPID, []string{goodID, badID})
	if err == nil {
		t.Fatal("expected an error naming the failed delete")
	}
	if len(deleted) != 2 {
		t.Fatalf("expected both IDs to be attempted, got %v", deleted)
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("error does not name the failed ID %s: %v", badID, err)
	}
}

// --- IP availability ---------------------------------------------------------

func TestRunIPAvailabilityList_SendsFiltersAndTruncatesNothing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/network-ip-availabilities", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"network_ip_availabilities": [
		  {"network_id": "` + extNetworkID + `", "network_name": "public", "total_ips": 253, "used_ips": 7}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	err := runIPAvailabilityList(context.Background(), networkClient(fakeServer), o, 4,
		"66666666-6666-6666-6666-666666666666", &out)
	if err != nil {
		t.Fatalf("runIPAvailabilityList returned error: %v", err)
	}
	if !strings.Contains(gotQuery, "ip_version=4") {
		t.Errorf("ip_version filter missing from query: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "project_id=66666666") {
		t.Errorf("project_id filter missing from query: %q", gotQuery)
	}
	if !strings.Contains(out.String(), "public") || !strings.Contains(out.String(), "253") {
		t.Errorf("rendered output missing expected fields:\n%s", out.String())
	}
}

func TestRunIPAvailabilityList_ZeroIPVersionOmitsFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/network-ip-availabilities", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "ip_version=") {
			t.Errorf("ip_version should be omitted when --ip-version was not given: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"network_ip_availabilities": []}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runIPAvailabilityList(context.Background(), networkClient(fakeServer), o, 0, "", &out); err != nil {
		t.Fatalf("runIPAvailabilityList returned error: %v", err)
	}
}

func TestRunIPAvailabilityList_ErrorOnMalformedBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/network-ip-availabilities", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "table"}
	if err := runIPAvailabilityList(context.Background(), networkClient(fakeServer), o, 0, "", &out); err == nil {
		t.Fatal("expected an error from a malformed response body")
	}
}
