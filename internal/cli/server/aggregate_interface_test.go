package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --- shared fakes for cross-service (auth.Client) seams ---------------------
//
// runServerAddPort/runServerRemovePort/runServerRemoveNetwork/runServerAddNetwork
// and runServerRescue (with --image) derive a second service client from an
// *auth.Client via ac.Network()/ac.Image(). Those factory methods build the
// client from Provider+Endpoint alone (no Keystone round trip needed) as long
// as Provider carries a working EndpointLocator, so a real auth session is
// not required. runHypervisorGauge instead calls ac.Placement(), whose
// implementation reads the unexported *Options this package cannot set from
// outside internal/auth — so its fake instead makes the locator fail,
// exercising the documented best-effort fallback rather than the happy path.

// authClientWithLocator returns an *auth.Client whose Provider resolves every
// service endpoint via loc, without a Keystone token request.
func authClientWithLocator(loc gophercloud.EndpointLocator) *auth.Client {
	return &auth.Client{
		Provider: &gophercloud.ProviderClient{
			TokenID:         fakeclient.TokenID,
			EndpointLocator: loc,
		},
	}
}

// fakeAuthClient resolves every service to fakeServer's own endpoint. Safe for
// Network()/Image(): neither touches the unexported *Options field, so a
// manually built *auth.Client works for them.
func fakeAuthClient(fakeServer th.FakeServer) *auth.Client {
	return authClientWithLocator(func(gophercloud.EndpointOpts) (string, error) {
		return fakeServer.Endpoint(), nil
	})
}

// authClientNoCatalog fails every endpoint lookup, as a cloud with no
// placement entry in the catalog would. Client.Placement() returns that error
// before ever reaching the unexported *Options field, so this is the only
// externally-constructible *auth.Client that Placement() can call safely.
func authClientNoCatalog() *auth.Client {
	return authClientWithLocator(func(eo gophercloud.EndpointOpts) (string, error) {
		return "", fmt.Errorf("%s endpoint not in catalog", eo.Type)
	})
}

// --- aggregate show -----------------------------------------------------

func TestRunAggregateShow_NumericID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/os-aggregates/9", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregate":{"id":9,"name":"agg-9","availability_zone":"az1",
			"hosts":["cmp-1","cmp-2"],"metadata":{"pinned":"true"}}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	// A numeric ref is used verbatim; no /os-aggregates listing call is made.
	if err := runAggregateShow(context.Background(), client, o, "9", &buf); err != nil {
		t.Fatalf("runAggregateShow: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/os-aggregates/9" {
		t.Errorf("request = %s %s, want GET /os-aggregates/9", gotMethod, gotPath)
	}
	out := buf.String()
	for _, want := range []string{"agg-9", "az1", "cmp-1, cmp-2", "pinned='true'"} {
		if !strings.Contains(out, want) {
			t.Errorf("aggregate show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunAggregateShow_ByName_ResolvesThenGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregates":[
			{"id":1,"name":"agg-other","hosts":[]},
			{"id":2,"name":"agg-target","hosts":[]}
		]}`))
	})
	fakeServer.Mux.HandleFunc("/os-aggregates/2", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregate":{"id":2,"name":"agg-target","hosts":[]}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runAggregateShow(context.Background(), client, o, "agg-target", &buf); err != nil {
		t.Fatalf("runAggregateShow: %v", err)
	}
	if !strings.Contains(buf.String(), "agg-target") {
		t.Errorf("output missing the resolved aggregate:\n%s", buf.String())
	}
}

func TestRunAggregateShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-aggregates/9", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runAggregateShow(context.Background(), client, o, "9", &buf); err == nil {
		t.Fatal("expected a 404 from nova to surface as an error")
	}
}

func TestResolveAggregateID_ZeroAndManyMatches(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregates":[
			{"id":1,"name":"dup","hosts":[]},
			{"id":2,"name":"dup","hosts":[]}
		]}`))
	})

	client := computeClient(fakeServer, "latest")
	if _, err := resolveAggregateID(context.Background(), client, "missing"); err == nil {
		t.Error("expected an error when no aggregate matches the name")
	}
	if _, err := resolveAggregateID(context.Background(), client, "dup"); err == nil {
		t.Error("expected an error when more than one aggregate shares the name")
	}
}

// --- aggregate set (sparse update opts) ----------------------------------

func TestRunAggregateSet_NameAndZoneOnly_SkipsMetadataAction(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var putBody map[string]any
	fakeServer.Mux.HandleFunc("/os-aggregates/7", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPut, r.Method)
		putBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregate":{"id":7,"name":"renamed","availability_zone":"az2","hosts":[]}}`))
	})
	// No handler registered for /os-aggregates/7/action: if the code sent a
	// set_metadata action despite --property being unset, the default 404
	// would surface as an error and fail this test.

	client := computeClient(fakeServer, "latest")
	f := &aggregateSetFlags{name: "renamed", zone: "az2"}
	if err := runAggregateSet(context.Background(), client, "7", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAggregateSet: %v", err)
	}
	agg, _ := putBody["aggregate"].(map[string]any)
	if agg["name"] != "renamed" || agg["availability_zone"] != "az2" {
		t.Errorf("PUT body aggregate = %v, want name=renamed availability_zone=az2", agg)
	}
}

func TestRunAggregateSet_PropertiesOnly_SkipsUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var actionBody map[string]any
	fakeServer.Mux.HandleFunc("/os-aggregates/7/action", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		actionBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregate":{"id":7,"name":"agg-7","hosts":[],"metadata":{"ssd":"true"}}}`))
	})
	// No handler at /os-aggregates/7 (PUT): a spurious Update call would 404.

	client := computeClient(fakeServer, "latest")
	f := &aggregateSetFlags{properties: []string{"ssd=true"}}
	if err := runAggregateSet(context.Background(), client, "7", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAggregateSet: %v", err)
	}
	sm, _ := actionBody["set_metadata"].(map[string]any)
	meta, _ := sm["metadata"].(map[string]any)
	if meta["ssd"] != "true" {
		t.Errorf("set_metadata body = %v, want metadata.ssd=true", actionBody)
	}
}

func TestRunAggregateSet_UpdateErrorPropagatesAndSkipsMetadata(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var actionCalled bool
	fakeServer.Mux.HandleFunc("/os-aggregates/7", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	fakeServer.Mux.HandleFunc("/os-aggregates/7/action", func(w http.ResponseWriter, _ *http.Request) {
		actionCalled = true
		w.WriteHeader(http.StatusOK)
	})

	client := computeClient(fakeServer, "latest")
	f := &aggregateSetFlags{zone: "az2", properties: []string{"ssd=true"}}
	if err := runAggregateSet(context.Background(), client, "7", f, &bytes.Buffer{}); err == nil {
		t.Fatal("expected the 500 from Update to surface as an error")
	}
	if actionCalled {
		t.Error("set_metadata action must not run after Update failed")
	}
}

// --- aggregate unset ------------------------------------------------------

func TestRunAggregateUnset_SendsNullPerKey(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var actionBody map[string]any
	fakeServer.Mux.HandleFunc("/os-aggregates/3/action", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		actionBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregate":{"id":3,"name":"agg-3","hosts":[],"metadata":{}}}`))
	})

	client := computeClient(fakeServer, "latest")
	err := runAggregateUnset(context.Background(), client, "3", []string{"ssd", "gpu"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runAggregateUnset: %v", err)
	}
	sm, _ := actionBody["set_metadata"].(map[string]any)
	meta, _ := sm["metadata"].(map[string]any)
	if _, ok := meta["ssd"]; !ok || meta["ssd"] != nil {
		t.Errorf("metadata.ssd = %v, want present and null", meta["ssd"])
	}
	if _, ok := meta["gpu"]; !ok || meta["gpu"] != nil {
		t.Errorf("metadata.gpu = %v, want present and null", meta["gpu"])
	}
}

func TestRunAggregateUnset_EmptyPropertiesIsNoop(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No handlers registered at all: any HTTP call would fail the test via a
	// default 404 turning into a returned error.

	client := computeClient(fakeServer, "latest")
	if err := runAggregateUnset(context.Background(), client, "whatever", nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAggregateUnset with no --property should be a pure no-op: %v", err)
	}
}

// --- aggregate delete ------------------------------------------------------

func TestRunAggregateDelete_BatchDeletesEveryRef(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	for _, id := range []string{"1", "2"} {
		fakeServer.Mux.HandleFunc("/os-aggregates/"+id, func(w http.ResponseWriter, r *http.Request) {
			th.AssertEquals(t, http.MethodDelete, r.Method)
			deleted = append(deleted, id)
			w.WriteHeader(http.StatusOK)
		})
	}

	client := computeClient(fakeServer, "latest")
	var buf bytes.Buffer
	if err := runAggregateDelete(context.Background(), client, []string{"1", "2"}, &buf); err != nil {
		t.Fatalf("runAggregateDelete: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want both refs attempted", deleted)
	}
	for _, want := range []string{"Deleted aggregate 1", "Deleted aggregate 2"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunAggregateDelete_PartialFailureStillAttemptsAll(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var secondCalled bool
	fakeServer.Mux.HandleFunc("/os-aggregates/1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/os-aggregates/2", func(w http.ResponseWriter, _ *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
	})

	client := computeClient(fakeServer, "latest")
	var buf bytes.Buffer
	err := runAggregateDelete(context.Background(), client, []string{"1", "2"}, &buf)
	if err == nil {
		t.Fatal("expected an error naming the failed ref")
	}
	if !secondCalled {
		t.Error("the second ref must still be attempted after the first failed")
	}
	if !strings.Contains(buf.String(), "Deleted aggregate 2") {
		t.Errorf("output missing the successful delete:\n%s", buf.String())
	}
}

// --- aggregate remove host --------------------------------------------------

func TestRunAggregateRemoveHost_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-aggregates/5/action", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregate":{"id":5,"name":"agg-a","hosts":[],"metadata":{}}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runAggregateRemoveHost(context.Background(), client, o, "5", "cmp-1", &buf); err != nil {
		t.Fatalf("runAggregateRemoveHost: %v", err)
	}
	rh, _ := gotBody["remove_host"].(map[string]any)
	if rh["host"] != "cmp-1" {
		t.Errorf("remove_host body = %v, want host=cmp-1", gotBody)
	}
	if !strings.Contains(buf.String(), "agg-a") {
		t.Errorf("remove host output missing the aggregate:\n%s", buf.String())
	}
}

func TestRunAggregateRemoveHost_ActionError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-aggregates/5/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runAggregateRemoveHost(context.Background(), client, o, "5", "cmp-1", &buf)
	if err == nil {
		t.Fatal("expected the 409 from nova to surface as an error")
	}
}

// --- server group show -------------------------------------------------

func TestRunServerGroupShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/os-server-groups/"+serverGroupID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_group": {"id": "` + serverGroupID + `", "name": "web",
			"policy": "anti-affinity", "rules": {"max_server_per_host": 2}, "members": ["m-1"]}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runServerGroupShow(context.Background(), client, o, serverGroupID, &buf); err != nil {
		t.Fatalf("runServerGroupShow: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/os-server-groups/"+serverGroupID {
		t.Errorf("request = %s %s, want GET /os-server-groups/%s", gotMethod, gotPath, serverGroupID)
	}
	out := buf.String()
	for _, want := range []string{serverGroupID, "anti-affinity", "m-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("server group show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunServerGroupShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-server-groups/"+serverGroupID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runServerGroupShow(context.Background(), client, o, serverGroupID, &buf); err == nil {
		t.Fatal("expected a 404 from nova to surface as an error")
	}
}

func TestRunServerGroupShow_UnknownNameFallsBackToLiteralAnd404s(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_groups": []}`))
	})
	var gotPath string
	fakeServer.Mux.HandleFunc("/os-server-groups/no-such-group", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNotFound)
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	// resolveServerGroupID's documented zero-match fallback: the literal name
	// is passed through to Get, which then 404s.
	if err := runServerGroupShow(context.Background(), client, o, "no-such-group", &buf); err == nil {
		t.Fatal("expected the fallback Get to 404")
	}
	if gotPath != "/os-server-groups/no-such-group" {
		t.Errorf("Get path = %q, want the literal name passed through", gotPath)
	}
}

// --- server rescue -----------------------------------------------------

func TestRunServerRescue_GeneratedPasswordNoImage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"adminPass":"gEnerated123!"}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &rescueFlags{}
	var buf bytes.Buffer
	// f.image is empty, so resolveRescueImageID never touches ac: nil is safe.
	if err := runServerRescue(context.Background(), client, nil, o, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerRescue: %v", err)
	}
	rescue, _ := gotBody["rescue"].(map[string]any)
	if _, present := rescue["adminPass"]; present {
		t.Errorf("adminPass sent although none was requested: %#v", rescue)
	}
	if _, present := rescue["rescue_image_ref"]; present {
		t.Errorf("rescue_image_ref sent although --image was empty: %#v", rescue)
	}
	if !strings.Contains(buf.String(), "gEnerated123!") {
		t.Errorf("output missing the generated adminPass:\n%s", buf.String())
	}
}

func TestRunServerRescue_ExplicitPasswordAndImage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const rescueImageID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"adminPass":"Str0ngPass!"}`))
	})

	client := computeClient(fakeServer, "latest")
	// --image is a UUID, so resolve.ImageID short-circuits without an HTTP call
	// to glance; only ac.Image() itself needs to succeed.
	ac := fakeAuthClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &rescueFlags{password: "Str0ngPass!", image: rescueImageID}
	var buf bytes.Buffer
	if err := runServerRescue(context.Background(), client, ac, o, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerRescue: %v", err)
	}
	rescue, _ := gotBody["rescue"].(map[string]any)
	if rescue["adminPass"] != "Str0ngPass!" || rescue["rescue_image_ref"] != rescueImageID {
		t.Errorf("rescue body = %v, want adminPass=Str0ngPass! rescue_image_ref=%s", rescue, rescueImageID)
	}
}

func TestRunServerRescue_ActionError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &rescueFlags{}
	var buf bytes.Buffer
	if err := runServerRescue(context.Background(), client, nil, o, serverUUID, f, &buf); err == nil {
		t.Fatal("expected the 409 from nova to surface as an error")
	}
}

// --- server add/remove port and network (outer, cross-service seams) ------

func TestRunServerAddPort_SendsPortIDAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		gotBody = decodeAttachBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"interfaceAttachment": {"port_id": "` + ifPortID + `", "net_id": "` + ifNetworkID + `",
			"mac_addr": "fa:16:3e:00:00:09", "port_state": "ACTIVE", "fixed_ips": []}}`))
	})

	s := &computeSession{client: computeClient(fakeServer, "latest"), auth: fakeAuthClient(fakeServer)}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	// portRef is a UUID, so resolveNetworkResource never issues a neutron HTTP
	// call; ac.Network() only needs to build successfully.
	if err := runServerAddPort(context.Background(), s, o, ifServerID, ifPortID, "", &buf); err != nil {
		t.Fatalf("runServerAddPort: %v", err)
	}
	inner, _ := gotBody["interfaceAttachment"].(map[string]any)
	if inner["port_id"] != ifPortID {
		t.Errorf("interfaceAttachment body = %v, want port_id=%s", inner, ifPortID)
	}
	if _, present := inner["net_id"]; present {
		t.Errorf("net_id sent alongside port_id; nova rejects both together: %#v", inner)
	}
	if !strings.Contains(buf.String(), ifPortID) {
		t.Errorf("output missing the attached port:\n%s", buf.String())
	}
}

func TestRunServerAddPort_AttachError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	s := &computeSession{client: computeClient(fakeServer, "latest"), auth: fakeAuthClient(fakeServer)}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runServerAddPort(context.Background(), s, o, ifServerID, ifPortID, "", &buf)
	if err == nil {
		t.Fatal("expected the 400 from nova to surface as an error")
	}
	if !strings.Contains(err.Error(), ifPortID) {
		t.Errorf("error %q does not name the port", err)
	}
}

func TestRunServerRemovePort_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface/"+ifPortID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "latest")
	ac := fakeAuthClient(fakeServer)
	var buf bytes.Buffer
	if err := runServerRemovePort(context.Background(), client, ac, ifServerID, ifPortID, &buf); err != nil {
		t.Fatalf("runServerRemovePort: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/servers/"+ifServerID+"/os-interface/"+ifPortID {
		t.Errorf("request = %s %s, want DELETE .../os-interface/%s", gotMethod, gotPath, ifPortID)
	}
	if !strings.Contains(buf.String(), ifPortID) {
		t.Errorf("output missing the detached port:\n%s", buf.String())
	}
}

func TestRunServerRemovePort_DeleteError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface/"+ifPortID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := computeClient(fakeServer, "latest")
	ac := fakeAuthClient(fakeServer)
	var buf bytes.Buffer
	err := runServerRemovePort(context.Background(), client, ac, ifServerID, ifPortID, &buf)
	if err == nil {
		t.Fatal("expected the 404 from nova to surface as an error")
	}
}

func TestRunServerAddNetwork_ResolvesThenAttaches(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeAttachBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"interfaceAttachment": {"port_id": "` + ifPortID + `", "net_id": "` + ifNetworkID + `",
			"mac_addr": "fa:16:3e:00:00:0a", "port_state": "ACTIVE", "fixed_ips": []}}`))
	})

	s := &computeSession{client: computeClient(fakeServer, "latest"), auth: fakeAuthClient(fakeServer)}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	// networkRef is a UUID, so ac.Network() only needs to build successfully;
	// resolve.NetworkID never issues an HTTP call.
	err := runServerAddNetwork(context.Background(), s, o, ifServerID, ifNetworkID, &attachFlags{}, &buf)
	if err != nil {
		t.Fatalf("runServerAddNetwork: %v", err)
	}
	inner, _ := gotBody["interfaceAttachment"].(map[string]any)
	if inner["net_id"] != ifNetworkID {
		t.Errorf("interfaceAttachment body = %v, want net_id=%s", inner, ifNetworkID)
	}
}

func TestRunServerAddNetwork_AttachErrorIsWrappedWithRefs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	s := &computeSession{client: computeClient(fakeServer, "latest"), auth: fakeAuthClient(fakeServer)}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runServerAddNetwork(context.Background(), s, o, ifServerID, ifNetworkID, &attachFlags{}, &buf)
	if err == nil {
		t.Fatal("expected the 400 from nova to surface as an error")
	}
	if !strings.Contains(err.Error(), ifServerID) || !strings.Contains(err.Error(), ifNetworkID) {
		t.Errorf("error %q does not name both server and network", err)
	}
}

func TestRunServerRemoveNetwork_ResolvesThenDetachesMatchingPorts(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveInterfaceList(fakeServer)
	var deleted []string
	fakeServer.Mux.HandleFunc("/servers/"+ifServerID+"/os-interface/", func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/servers/"+ifServerID+"/os-interface/"))
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "latest")
	ac := fakeAuthClient(fakeServer)
	var buf bytes.Buffer
	if err := runServerRemoveNetwork(context.Background(), client, ac, ifServerID, ifNetworkID, &buf); err != nil {
		t.Fatalf("runServerRemoveNetwork: %v", err)
	}
	th.AssertDeepEquals(t, []string{ifPortID}, deleted)
	if !strings.Contains(buf.String(), "1 port(s)") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

func TestRunServerRemoveNetwork_Outer_NoMatchIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveInterfaceList(fakeServer)

	client := computeClient(fakeServer, "latest")
	ac := fakeAuthClient(fakeServer)
	var buf bytes.Buffer
	err := runServerRemoveNetwork(context.Background(), client, ac, ifServerID,
		"99999999-9999-9999-9999-999999999999", &buf)
	if err == nil {
		t.Fatal("expected an error when the server has no interface on that network")
	}
}

// --- hypervisor gauge ----------------------------------------------------

const hvGaugeDetailBody = `{"hypervisors":[
	{"id":"1","hypervisor_hostname":"cmp1","hypervisor_type":"QEMU","hypervisor_version":2010000,
	 "state":"up","status":"enabled","host_ip":"192.0.2.11","vcpus":32,"vcpus_used":8,
	 "memory_mb":131072,"memory_mb_used":16384,"free_ram_mb":114688,"local_gb":500,"local_gb_used":100,
	 "free_disk_gb":400,"disk_available_least":380,"running_vms":4,"current_workload":0,
	 "cpu_info":{"vendor":"Intel","arch":"x86_64","model":"Skylake"},
	 "service":{"id":"7","host":"cmp1","disabled_reason":""}},
	{"id":"2","hypervisor_hostname":"cmp2","hypervisor_type":"QEMU","hypervisor_version":2010000,
	 "state":"down","status":"disabled","host_ip":"192.0.2.12","vcpus":16,"vcpus_used":0,
	 "memory_mb":65536,"memory_mb_used":0,"local_gb":200,"local_gb_used":0,
	 "cpu_info":{"vendor":"AMD","arch":"x86_64","model":"EPYC"},
	 "service":{"id":"8","host":"cmp2","disabled_reason":"maintenance"}}
]}`

// installHVGaugeFakes wires /os-hypervisors/detail and /os-aggregates for the
// gauge tests, and returns the assertion hook for the microversion header
// gatherHypervisorRows must pin to empty (nova dropped the usage fields at
// 2.88).
func installHVGaugeFakes(t *testing.T, fakeServer th.FakeServer) (getVer *string) {
	t.Helper()
	var v string
	fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, r *http.Request) {
		v = r.Header.Get("OpenStack-API-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hvGaugeDetailBody))
	})
	fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregates":[{"id":1,"name":"az-1","hosts":["cmp1","cmp2"]}]}`))
	})
	return &v
}

func TestRunHypervisorGauge_TableFormat_PlacementUnavailableIsBestEffort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	gotVer := installHVGaugeFakes(t, fakeServer)

	compute := computeClient(fakeServer, "latest")
	// authClientNoCatalog: placement is unreachable, which must not fail the
	// command — the nova-sourced numbers are used as-is.
	ac := authClientNoCatalog()
	o := &output.Options{Format: output.FormatTable}
	g := &gaugeOpts{warnPct: 70, critPct: 90, sortKey: "name", width: 200}
	var buf bytes.Buffer
	if err := runHypervisorGauge(context.Background(), compute, ac, o, g, &buf); err != nil {
		t.Fatalf("runHypervisorGauge: %v", err)
	}
	if *gotVer != "" {
		t.Errorf("OpenStack-API-Version = %q, want empty (nova 2.88 dropped usage fields)", *gotVer)
	}
	out := buf.String()
	for _, want := range []string{"cmp1", "cmp2", "az-1", "DOWN", "2 hypervisors"} {
		if !strings.Contains(out, want) {
			t.Errorf("gauge table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunHypervisorGauge_NonTableFormat_UsesRawTable(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	installHVGaugeFakes(t, fakeServer)

	compute := computeClient(fakeServer, "latest")
	ac := authClientNoCatalog()
	o := &output.Options{Format: output.FormatValue}
	g := &gaugeOpts{warnPct: 70, critPct: 90, sortKey: "name", width: 200}
	var buf bytes.Buffer
	if err := runHypervisorGauge(context.Background(), compute, ac, o, g, &buf); err != nil {
		t.Fatalf("runHypervisorGauge: %v", err)
	}
	out := buf.String()
	// Raw (non-gauge) rendering carries the plain numbers rather than bars.
	for _, want := range []string{"cmp1", "Skylake", "cmp2", "EPYC"} {
		if !strings.Contains(out, want) {
			t.Errorf("gauge raw-table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunHypervisorGauge_ListError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	compute := computeClient(fakeServer, "latest")
	ac := authClientNoCatalog()
	o := &output.Options{Format: output.FormatTable}
	g := &gaugeOpts{warnPct: 70, critPct: 90, sortKey: "name", width: 200}
	var buf bytes.Buffer
	if err := runHypervisorGauge(context.Background(), compute, ac, o, g, &buf); err == nil {
		t.Fatal("expected the 500 listing hypervisors to surface as an error")
	}
}
