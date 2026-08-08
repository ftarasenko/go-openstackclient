package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	ifServerID  = "11111111-1111-1111-1111-111111111111"
	ifPortID    = "22222222-2222-2222-2222-222222222222"
	ifNetworkID = "33333333-3333-3333-3333-333333333333"
)

// serveInterfaceList registers GET /servers/{id}/os-interface with two
// interfaces on different networks.
func serveInterfaceList(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"interfaceAttachments": [
		  {"port_id": "` + ifPortID + `", "net_id": "` + ifNetworkID + `", "mac_addr": "fa:16:3e:00:00:01",
		   "port_state": "ACTIVE", "fixed_ips": [{"ip_address": "192.0.2.10", "subnet_id": "sub-1"}]},
		  {"port_id": "44444444-4444-4444-4444-444444444444", "net_id": "55555555-5555-5555-5555-555555555555",
		   "mac_addr": "fa:16:3e:00:00:02", "port_state": "ACTIVE",
		   "fixed_ips": [{"ip_address": "198.51.100.10", "subnet_id": "sub-2"}]}
		]}`))
	})
}

func TestAttachOpts_BodyMatchesNovaSchema(t *testing.T) {
	// nova's attach_interfaces schema: net_id/port_id/fixed_ips (max one item,
	// ip_address required) plus tag from 2.49, additionalProperties=false.
	opts := attachOpts{
		CreateOpts: attachinterfaces.CreateOpts{
			NetworkID: ifNetworkID,
			FixedIPs:  []attachinterfaces.FixedIP{{IPAddress: "192.0.2.10"}},
		},
		Tag: "data",
	}
	body, err := opts.ToAttachInterfacesCreateMap()
	if err != nil {
		t.Fatalf("ToAttachInterfacesCreateMap returned error: %v", err)
	}
	inner := body["interfaceAttachment"].(map[string]any)
	th.AssertEquals(t, ifNetworkID, inner["net_id"])
	th.AssertEquals(t, "data", inner["tag"])
	fixed := inner["fixed_ips"].([]any)
	th.AssertEquals(t, 1, len(fixed))
	th.AssertEquals(t, "192.0.2.10", fixed[0].(map[string]any)["ip_address"])
	// A fixed IP carrying subnet_id would be rejected: the schema allows only
	// ip_address inside fixed_ips.
	if _, present := fixed[0].(map[string]any)["subnet_id"]; present {
		t.Errorf("subnet_id sent inside fixed_ips, which nova's schema rejects: %#v", fixed[0])
	}
}

func TestNewAttachOpts_TagBelow249IsRejectedLocally(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := computeClient(fakeServer, "2.40")
	_, err := newAttachOpts(client, attachinterfaces.CreateOpts{PortID: ifPortID}, "data")
	if err == nil {
		t.Fatal("expected --tag to be rejected when pinned below 2.49")
	}
	if !strings.Contains(err.Error(), "2.49") {
		t.Errorf("error %q does not name the required microversion", err)
	}
}

func TestNewAttachOpts_NoTagKeepsTheTypedOpts(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := computeClient(fakeServer, "2.40")
	opts, err := newAttachOpts(client, attachinterfaces.CreateOpts{PortID: ifPortID}, "")
	if err != nil {
		t.Fatalf("newAttachOpts returned error: %v", err)
	}
	body, err := opts.ToAttachInterfacesCreateMap()
	if err != nil {
		t.Fatalf("ToAttachInterfacesCreateMap returned error: %v", err)
	}
	inner := body["interfaceAttachment"].(map[string]any)
	if _, present := inner["tag"]; present {
		t.Errorf("tag present although none was requested: %#v", inner)
	}
}

func TestRunServerRemoveNetwork_DetachesOnlyMatchingPorts(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveInterfaceList(fakeServer)
	var deleted []string
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface/", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "DELETE", r.Method)
		deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/servers/"+ifServerID+"/os-interface/"))
		w.WriteHeader(http.StatusAccepted)
	})

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	// networkResolver is bypassed by passing a UUID; nova's delete endpoint is
	// keyed by port, so the network match happens client-side.
	err := runServerRemoveNetworkForID(context.Background(), client, ifServerID, ifNetworkID, &out)
	if err != nil {
		t.Fatalf("runServerRemoveNetwork returned error: %v", err)
	}
	th.AssertDeepEquals(t, []string{ifPortID}, deleted)
	if !strings.Contains(out.String(), "1 port(s)") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestRunServerRemoveNetwork_NoMatchIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveInterfaceList(fakeServer)

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	err := runServerRemoveNetworkForID(context.Background(), client, ifServerID,
		"99999999-9999-9999-9999-999999999999", &out)
	if err == nil {
		t.Fatal("expected an error when the server has no interface on that network")
	}
}

func TestRunServerRemoveFixedIP_MatchesTheAddress(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveInterfaceList(fakeServer)
	var deleted []string
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface/", func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/servers/"+ifServerID+"/os-interface/"))
		w.WriteHeader(http.StatusAccepted)
	})

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	// Nova has no delete-by-address endpoint, so the address is matched against
	// the interface listing and the owning port is detached.
	if err := runServerRemoveFixedIP(context.Background(), client, ifServerID, "198.51.100.10", &out); err != nil {
		t.Fatalf("runServerRemoveFixedIP returned error: %v", err)
	}
	th.AssertDeepEquals(t, []string{"44444444-4444-4444-4444-444444444444"}, deleted)
}

func TestRunServerRemoveFixedIP_UnknownAddressIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveInterfaceList(fakeServer)

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	err := runServerRemoveFixedIP(context.Background(), client, ifServerID, "203.0.113.99", &out)
	if err == nil {
		t.Fatal("expected an error for an address the server does not hold")
	}
}

func TestWriteInterface_RendersFixedIPs(t *testing.T) {
	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	iface := &attachinterfaces.Interface{
		PortID: ifPortID, NetID: ifNetworkID, MACAddr: "fa:16:3e:00:00:01", PortState: "ACTIVE",
		FixedIPs: []attachinterfaces.FixedIP{{IPAddress: "192.0.2.10", SubnetID: "sub-1"}},
	}
	if err := writeInterface(o, &out, iface); err != nil {
		t.Fatalf("writeInterface returned error: %v", err)
	}
	if !strings.Contains(out.String(), "192.0.2.10") {
		t.Errorf("output missing the fixed IP:\n%s", out.String())
	}
}

// decodeAttachBody is a small helper for asserting on a captured request body.
func decodeAttachBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decoding request body: %v", err)
	}
	return body
}

func TestRunServerAddNetwork_SendsNetIDAndFixedIP(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body = decodeAttachBody(t, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"interfaceAttachment": {"port_id": "` + ifPortID + `", "net_id": "` + ifNetworkID + `",
		  "mac_addr": "fa:16:3e:00:00:01", "port_state": "ACTIVE", "fixed_ips": [{"ip_address": "192.0.2.10"}]}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := computeClient(fakeServer, "latest")
	err := runServerAddNetworkForID(context.Background(), client, o, ifServerID, ifNetworkID, "192.0.2.10", "", &out)
	if err != nil {
		t.Fatalf("runServerAddNetwork returned error: %v", err)
	}
	inner := body["interfaceAttachment"].(map[string]any)
	th.AssertEquals(t, ifNetworkID, inner["net_id"])
	th.AssertEquals(t, "192.0.2.10", inner["fixed_ips"].([]any)[0].(map[string]any)["ip_address"])
	if _, present := inner["port_id"]; present {
		t.Errorf("port_id sent alongside net_id; nova rejects both together: %#v", inner)
	}
}
