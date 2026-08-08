package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const readoutNodeID = "11111111-1111-1111-1111-111111111111"

func TestRunNodeValidate_RendersEveryInterface(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/validate", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "power":  {"result": true,  "reason": ""},
		  "deploy": {"result": false, "reason": "Cannot validate image information"},
		  "console": {"result": false, "reason": ""}
		}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeValidate(context.Background(), client, o, readoutNodeID, &out); err != nil {
		t.Fatalf("runNodeValidate returned error: %v", err)
	}

	th.AssertEquals(t, "GET", gotMethod)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// Every interface gets a row, including the ones ironic did not report on —
	// a driver that omits an interface is information, not a reason to hide it.
	th.AssertEquals(t, 12, len(lines))
	if !strings.Contains(out.String(), "power\ttrue") {
		t.Errorf("output missing the power row:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Cannot validate image information") {
		t.Errorf("output missing the deploy failure reason:\n%s", out.String())
	}
}

func TestRunNodeVIFList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotMicroversion string
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/vifs", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotMicroversion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"vifs": [{"id": "22222222-2222-2222-2222-222222222222"}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeVIFList(context.Background(), client, o, readoutNodeID, &out); err != nil {
		t.Fatalf("runNodeVIFList returned error: %v", err)
	}

	th.AssertEquals(t, "GET", gotMethod)
	th.AssertEquals(t, "latest", gotMicroversion)
	th.AssertEquals(t, "22222222-2222-2222-2222-222222222222\n", out.String())
}

func TestRunNodeVIFAttach_BodyIncludesPortAndInfo(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/vifs", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	err := runNodeVIFAttach(context.Background(), client, readoutNodeID,
		"22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "",
		[]string{"tenant_vif_port_id=44444444-4444-4444-4444-444444444444"}, &out)
	if err != nil {
		t.Fatalf("runNodeVIFAttach returned error: %v", err)
	}

	th.AssertEquals(t, "POST", gotMethod)
	th.AssertEquals(t, "22222222-2222-2222-2222-222222222222", body["id"])
	th.AssertEquals(t, "33333333-3333-3333-3333-333333333333", body["port_uuid"])
	th.AssertEquals(t, "44444444-4444-4444-4444-444444444444", body["tenant_vif_port_id"])
}

func TestVifAttachOpts_InfoCannotOverrideID(t *testing.T) {
	opts := vifAttachOpts{
		VirtualInterfaceOpts: nodes.VirtualInterfaceOpts{ID: "22222222-2222-2222-2222-222222222222"},
		Info:                 map[string]any{"id": "spoofed"},
	}
	if _, err := opts.ToVirtualInterfaceMap(); err == nil {
		t.Fatal("expected --vif-info to be rejected when it collides with a reserved key")
	}
}

func TestRunNodeVIFDetach_UsesVIFPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const vifID = "22222222-2222-2222-2222-222222222222"
	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/vifs/"+vifID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeVIFDetach(context.Background(), client, readoutNodeID, vifID, &out); err != nil {
		t.Fatalf("runNodeVIFDetach returned error: %v", err)
	}
	th.AssertEquals(t, "DELETE", gotMethod)
	th.AssertEquals(t, "Detached VIF "+vifID+" from node "+readoutNodeID+"\n", out.String())
}

func TestRunNodeBIOSSettingList_DetailQueryAndBlankOptionals(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/bios", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		// read_only is absent, as it is on any cloud below API 1.74.
		_, _ = w.Write([]byte(`{"bios": [
		  {"name": "SriovGlobalEnable", "value": "Disabled", "attribute_type": "Enumeration"},
		  {"name": "BootMode", "value": "Uefi", "attribute_type": "Enumeration", "read_only": false}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeBIOSSettingList(context.Background(), client, o, readoutNodeID, true, &out); err != nil {
		t.Fatalf("runNodeBIOSSettingList returned error: %v", err)
	}

	th.AssertEquals(t, "detail=true", gotQuery)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	th.AssertEquals(t, 2, len(lines))
	// Sorted by name, so BootMode comes first regardless of the API's order.
	if !strings.HasPrefix(lines[0], "BootMode\t") {
		t.Errorf("rows are not sorted by name: %q", lines[0])
	}
	// An absent optional must render blank, not as "false".
	if strings.Contains(lines[1], "false") {
		t.Errorf("absent read_only rendered as a zero value: %q", lines[1])
	}
}

func TestRunNodeBIOSSettingShow_SingleSetting(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/bios/BootMode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"BootMode": {"name": "BootMode", "value": "Uefi", "read_only": true}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeBIOSSettingShow(context.Background(), client, o, readoutNodeID, "BootMode", &out); err != nil {
		t.Fatalf("runNodeBIOSSettingShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Uefi") || !strings.Contains(out.String(), "true") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestRunNodeFirmwareList_RendersComponents(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/firmware", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firmware": [
		  {"component": "bmc", "initial_version": "1.0.0", "current_version": "1.2.0", "updated_at": null}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeFirmwareList(context.Background(), client, o, readoutNodeID, &out); err != nil {
		t.Fatalf("runNodeFirmwareList returned error: %v", err)
	}
	if !strings.Contains(out.String(), "bmc\t1.0.0\t1.2.0") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestRunNodeFirmwareList_ExplainsMicroversionOnZed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// On Zed the route does not exist at all, so ironic answers a bare 404 —
	// which without the guard reads as "no such node".
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/firmware", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ironicMaxVersionHeader, "1.82")
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	err := runNodeFirmwareList(context.Background(), client, o, readoutNodeID, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"requires ironic API 1.86", "OpenStack 2023.2", "supports up to 1.82"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRunNodeFirmwareList_KeepsPlain404OnNewCloud(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// A 1.90 cloud returning 404 means the node is missing, not the feature.
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/firmware", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ironicMaxVersionHeader, "1.90")
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	err := runNodeFirmwareList(context.Background(), client, o, readoutNodeID, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "requires ironic API") {
		t.Errorf("error %q should not claim a microversion problem on a 1.90 cloud", err)
	}
}

func TestExplainMicroversion_FallsBackToVersionDocument(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// No maximum-version header on the error response, so the guard has to read
	// the v1 version document instead.
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/firmware", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "v1", "version": {"id": "v1", "version": "1.82", "min_version": "1.1"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	err := runNodeFirmwareList(context.Background(), client, o, readoutNodeID, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "supports up to 1.82") {
		t.Errorf("error %q did not pick the maximum up from the version document", err)
	}
}

func TestCompareMicroversions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.82", "1.86", -1},
		{"1.86", "1.86", 0},
		{"1.90", "1.86", 1},
		{"1.9", "1.86", -1}, // 9 < 86: minor is numeric, not lexicographic
		{"2.1", "1.99", 1},
		{"garbage", "1.86", -1}, // unparseable must never suppress the explanation
	}
	for _, c := range cases {
		if got := compareMicroversions(c.a, c.b); got != c.want {
			t.Errorf("compareMicroversions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRunNodeInjectNMI_PostsToManagementEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/"+readoutNodeID+"/management/inject_nmi", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeInjectNMI(context.Background(), client, readoutNodeID, &out); err != nil {
		t.Fatalf("runNodeInjectNMI returned error: %v", err)
	}
	th.AssertEquals(t, "PUT", gotMethod)
	th.AssertEquals(t, "Injected NMI into node "+readoutNodeID+"\n", out.String())
}

func TestExtractBIOSSetting_AcceptsBothKeyings(t *testing.T) {
	// Ironic keys the object by the setting name; gophercloud's own fixture uses
	// the literal "Setting". Both have to decode, or `bios setting show` returns
	// a blank row against one of them.
	for name, body := range map[string]any{
		"ironic":      map[string]any{"BootMode": map[string]any{"name": "BootMode", "value": "Uefi"}},
		"gophercloud": map[string]any{"Setting": map[string]any{"name": "BootMode", "value": "Uefi"}},
	} {
		var res nodes.GetBIOSSettingResult
		res.Body = body
		s, err := extractBIOSSetting(res, "BootMode")
		if err != nil {
			t.Fatalf("%s keying: %v", name, err)
		}
		th.AssertEquals(t, "Uefi", s.Value)
	}
}
