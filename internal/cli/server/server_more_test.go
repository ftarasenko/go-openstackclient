package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serverUUID is a canonical UUID so resolveServerID uses it verbatim, sparing
// the tests from also mocking the /servers name-lookup call.
const serverUUID = "11111111-1111-1111-1111-111111111111"

// assertNovaMicroversion checks both microversion headers the nova client emits,
// mirroring the sibling tests in server_test.go.
func assertNovaMicroversion(t *testing.T, r *http.Request, want string) {
	t.Helper()
	if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != want {
		t.Errorf("X-OpenStack-Nova-API-Version = %q, want %q", got, want)
	}
	if got := r.Header.Get("OpenStack-API-Version"); got != "compute "+want {
		t.Errorf("OpenStack-API-Version = %q, want %q", got, "compute "+want)
	}
}

// decodeBody reads and JSON-decodes a request body into a generic map.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading request body: %v", err)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decoding request body %q: %v", string(body), err)
	}
	return m
}

func TestRunServerShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertNovaMicroversion(t, r, "2.79")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"server":{
			"id":"` + serverUUID + `",
			"name":"web-1",
			"status":"ACTIVE",
			"key_name":"mykey",
			"OS-EXT-AZ:availability_zone":"nova",
			"addresses":{"private":[{"addr":"10.0.0.5","version":4}]},
			"flavor":{"original_name":"m1.small"},
			"image":{"id":"img-123"}
		}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runServerShow(context.Background(), client, o, serverUUID, &buf); err != nil {
		t.Fatalf("runServerShow: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{serverUUID, "web-1", "ACTIVE", "mykey", "private=10.0.0.5", "m1.small", "img-123"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunServerCreate_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveFlavorRef lists flavors and matches the name → ID.
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})

	var gotMethod string
	var gotServer map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertNovaMicroversion(t, r, "2.79")
		body := decodeBody(t, r)
		gotServer, _ = body["server"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"web-3","status":"BUILD","adminPass":"s3cr3t"}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	f := &serverCreateFlags{image: "img-uuid", flavor: "m1.small"}

	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), client, o, "web-3", f, &buf); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotServer["name"] != "web-3" {
		t.Errorf("body server.name = %v, want web-3", gotServer["name"])
	}
	if gotServer["flavorRef"] != "2" {
		t.Errorf("body server.flavorRef = %v, want 2 (resolved from name)", gotServer["flavorRef"])
	}
	if gotServer["imageRef"] != "img-uuid" {
		t.Errorf("body server.imageRef = %v, want img-uuid", gotServer["imageRef"])
	}
	out := buf.String()
	for _, want := range []string{"new-id", "web-3", "BUILD", "s3cr3t"} {
		if !strings.Contains(out, want) {
			t.Errorf("create output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunServerCreate_FlavorRequired(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// --flavor is validated before any HTTP call, so no handlers are needed.
	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runServerCreate(context.Background(), client, o, "web-3", &serverCreateFlags{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "--flavor is required") {
		t.Fatalf("err = %v, want --flavor is required", err)
	}
}

func TestRunServerDelete_MultipleServers(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const other = "22222222-2222-2222-2222-222222222222"
	deleted := map[string]string{}
	for _, id := range []string{serverUUID, other} {
		id := id
		fakeServer.Mux.HandleFunc("/servers/"+id, func(w http.ResponseWriter, r *http.Request) {
			deleted[id] = r.Method
			w.WriteHeader(http.StatusNoContent)
		})
	}

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerDelete(context.Background(), client, []string{serverUUID, other}, &buf); err != nil {
		t.Fatalf("runServerDelete: %v", err)
	}
	if deleted[serverUUID] != http.MethodDelete || deleted[other] != http.MethodDelete {
		t.Errorf("methods = %v, want both DELETE", deleted)
	}
	out := buf.String()
	if !strings.Contains(out, "Deleted server "+serverUUID) || !strings.Contains(out, "Deleted server "+other) {
		t.Errorf("output missing delete confirmations:\n%s", out)
	}
}

func TestRunServerSet_NameAndProperties(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var putMethod string
	var putBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, r *http.Request) {
		putMethod = r.Method
		putBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","name":"renamed"}}`))
	})
	var metaMethod string
	var metaBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/metadata", func(w http.ResponseWriter, r *http.Request) {
		metaMethod = r.Method
		metaBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"metadata":{"env":"prod"}}`))
	})

	client := computeClient(fakeServer, "2.79")
	f := &serverSetFlags{name: "renamed", properties: []string{"env=prod"}}

	var buf bytes.Buffer
	if err := runServerSet(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerSet: %v", err)
	}
	if putMethod != http.MethodPut {
		t.Errorf("update method = %q, want PUT", putMethod)
	}
	if srv, _ := putBody["server"].(map[string]any); srv["name"] != "renamed" {
		t.Errorf("update body server.name = %v, want renamed", putBody["server"])
	}
	if metaMethod != http.MethodPost {
		t.Errorf("metadata method = %q, want POST", metaMethod)
	}
	if meta, _ := metaBody["metadata"].(map[string]any); meta["env"] != "prod" {
		t.Errorf("metadata body = %v, want env=prod", metaBody["metadata"])
	}
}

func TestRunServerUnset_RemovesEachProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	deleted := map[string]string{}
	for _, key := range []string{"env", "role"} {
		key := key
		fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/metadata/"+key, func(w http.ResponseWriter, r *http.Request) {
			deleted[key] = r.Method
			w.WriteHeader(http.StatusNoContent)
		})
	}

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerUnset(context.Background(), client, serverUUID, []string{"env", "role"}, &buf); err != nil {
		t.Fatalf("runServerUnset: %v", err)
	}
	if deleted["env"] != http.MethodDelete || deleted["role"] != http.MethodDelete {
		t.Errorf("methods = %v, want both DELETE", deleted)
	}
}

func TestRunServerReboot(t *testing.T) {
	cases := []struct {
		name     string
		method   servers.RebootMethod
		wantType string
	}{
		{"soft", servers.SoftReboot, "SOFT"},
		{"hard", servers.HardReboot, "HARD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod string
			var gotBody map[string]any
			fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				assertNovaMicroversion(t, r, "2.79")
				gotBody = decodeBody(t, r)
				w.WriteHeader(http.StatusAccepted)
			})

			client := computeClient(fakeServer, "2.79")
			var buf bytes.Buffer
			if err := runServerReboot(context.Background(), client, serverUUID, tc.method, &buf); err != nil {
				t.Fatalf("runServerReboot: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			reboot, _ := gotBody["reboot"].(map[string]any)
			if reboot["type"] != tc.wantType {
				t.Errorf("reboot.type = %v, want %s", reboot["type"], tc.wantType)
			}
			if !strings.Contains(buf.String(), "Rebooted server "+serverUUID) {
				t.Errorf("output = %q, want reboot confirmation", buf.String())
			}
		})
	}
}

func TestRunServerResize_ConfirmRevert(t *testing.T) {
	cases := []struct {
		name    string
		confirm bool
		revert  bool
		wantKey string
		wantOut string
	}{
		{"confirm", true, false, "confirmResize", "Confirmed resize"},
		{"revert", false, true, "revertResize", "Reverted resize"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotBody map[string]any
			fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
				gotBody = decodeBody(t, r)
				w.WriteHeader(http.StatusAccepted)
			})

			client := computeClient(fakeServer, "2.79")
			var buf bytes.Buffer
			if err := runServerResize(context.Background(), client, serverUUID, "", tc.confirm, tc.revert, &buf); err != nil {
				t.Fatalf("runServerResize: %v", err)
			}
			if _, ok := gotBody[tc.wantKey]; !ok {
				t.Errorf("body = %v, want key %q", gotBody, tc.wantKey)
			}
			if !strings.Contains(buf.String(), tc.wantOut) {
				t.Errorf("output = %q, want %q", buf.String(), tc.wantOut)
			}
		})
	}
}

func TestRunServerResize_ToFlavor(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavors":[{"id":"3","name":"m1.large"}]}`))
	})
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerResize(context.Background(), client, serverUUID, "m1.large", false, false, &buf); err != nil {
		t.Fatalf("runServerResize: %v", err)
	}
	resize, _ := gotBody["resize"].(map[string]any)
	if resize["flavorRef"] != "3" {
		t.Errorf("resize.flavorRef = %v, want 3 (resolved from name)", resize["flavorRef"])
	}
	if !strings.Contains(buf.String(), "Resized server") {
		t.Errorf("output = %q, want resize confirmation", buf.String())
	}
}

func TestRunServerRebuild_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","name":"web-1","status":"REBUILD"}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerRebuild(context.Background(), client, o, serverUUID, "img-new", &buf); err != nil {
		t.Fatalf("runServerRebuild: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	rebuild, _ := gotBody["rebuild"].(map[string]any)
	if rebuild["imageRef"] != "img-new" {
		t.Errorf("rebuild.imageRef = %v, want img-new", rebuild["imageRef"])
	}
	out := buf.String()
	for _, want := range []string{serverUUID, "web-1", "REBUILD"} {
		if !strings.Contains(out, want) {
			t.Errorf("rebuild output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunServerAddVolume_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-volume_attachments", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"volumeAttachment":{"id":"att-1","serverId":"` + serverUUID + `","volumeId":"vol-9","device":"/dev/vdb"}}`))
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerAddVolume(context.Background(), client, serverUUID, "vol-9", "/dev/vdb", &buf); err != nil {
		t.Fatalf("runServerAddVolume: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	att, _ := gotBody["volumeAttachment"].(map[string]any)
	if att["volumeId"] != "vol-9" || att["device"] != "/dev/vdb" {
		t.Errorf("volumeAttachment body = %v, want volumeId=vol-9 device=/dev/vdb", att)
	}
	if !strings.Contains(buf.String(), "Attached volume vol-9 to server "+serverUUID) {
		t.Errorf("output = %q, want attach confirmation", buf.String())
	}
}

func TestRunServerRemoveVolume_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-volume_attachments/vol-9", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runServerRemoveVolume(context.Background(), client, serverUUID, "vol-9", &buf); err != nil {
		t.Fatalf("runServerRemoveVolume: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(buf.String(), "Detached volume vol-9 from server "+serverUUID) {
		t.Errorf("output = %q, want detach confirmation", buf.String())
	}
}

func TestRunServerSecurityGroup_AddRemove(t *testing.T) {
	subtests := []struct {
		name    string
		add     bool
		wantKey string
		wantOut string
	}{
		{"add", true, "addSecurityGroup", "Added security group web to server "},
		{"remove", false, "removeSecurityGroup", "Removed security group web from server "},
	}
	for _, tc := range subtests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod string
			var gotBody map[string]any
			fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotBody = decodeBody(t, r)
				w.WriteHeader(http.StatusAccepted)
			})

			client := computeClient(fakeServer, "2.79")
			var buf bytes.Buffer
			var err error
			if tc.add {
				err = runServerAddSecurityGroup(context.Background(), client, serverUUID, "web", &buf)
			} else {
				err = runServerRemoveSecurityGroup(context.Background(), client, serverUUID, "web", &buf)
			}
			if err != nil {
				t.Fatalf("security group action: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			grp, _ := gotBody[tc.wantKey].(map[string]any)
			if grp["name"] != "web" {
				t.Errorf("body %s.name = %v, want web", tc.wantKey, grp)
			}
			if !strings.Contains(buf.String(), tc.wantOut+serverUUID) {
				t.Errorf("output = %q, want %q", buf.String(), tc.wantOut+serverUUID)
			}
		})
	}
}

func TestRunConsoleLogShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"output":"boot line 1\nboot line 2\n"}`))
	})

	client := computeClient(fakeServer, "2.79")
	var buf bytes.Buffer
	if err := runConsoleLogShow(context.Background(), client, serverUUID, 10, &buf); err != nil {
		t.Fatalf("runConsoleLogShow: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	getConsole, _ := gotBody["os-getConsoleOutput"].(map[string]any)
	if getConsole["length"].(float64) != 10 {
		t.Errorf("os-getConsoleOutput.length = %v, want 10", getConsole["length"])
	}
	if !strings.Contains(buf.String(), "boot line 1") || !strings.Contains(buf.String(), "boot line 2") {
		t.Errorf("output = %q, want console log lines", buf.String())
	}
}

func TestRunConsoleURLShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/remote-consoles", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"remote_console":{"type":"novnc","protocol":"vnc","url":"http://vnc.example/?token=abc"}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runConsoleURLShow(context.Background(), client, o, serverUUID, "novnc", &buf); err != nil {
		t.Fatalf("runConsoleURLShow: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	rc, _ := gotBody["remote_console"].(map[string]any)
	if rc["type"] != "novnc" || rc["protocol"] != "vnc" {
		t.Errorf("remote_console body = %v, want type=novnc protocol=vnc", rc)
	}
	out := buf.String()
	for _, want := range []string{"novnc", "vnc", "http://vnc.example/?token=abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("console url output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunComputeServiceSet_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveServiceID lists services filtered by host+binary.
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc-1","binary":"nova-compute","host":"cmp1"}]}`))
	})
	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-services/svc-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":{"id":"svc-1","binary":"nova-compute","host":"cmp1","status":"disabled","disabled_reason":"maintenance"}}`))
	})

	client := computeClient(fakeServer, "2.53")
	f := &serviceSetFlags{disable: true, disableReason: "maintenance", down: true}
	var buf bytes.Buffer
	if err := runComputeServiceSet(context.Background(), client, "cmp1", "nova-compute", f, &buf); err != nil {
		t.Fatalf("runComputeServiceSet: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotBody["status"] != "disabled" {
		t.Errorf("body.status = %v, want disabled", gotBody["status"])
	}
	if gotBody["disabled_reason"] != "maintenance" {
		t.Errorf("body.disabled_reason = %v, want maintenance", gotBody["disabled_reason"])
	}
	if gotBody["forced_down"] != true {
		t.Errorf("body.forced_down = %v, want true", gotBody["forced_down"])
	}
	if !strings.Contains(buf.String(), "Updated compute service nova-compute on host cmp1") {
		t.Errorf("output = %q, want update confirmation", buf.String())
	}
}

func TestRunComputeServiceSet_NothingToDo(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc-1","binary":"nova-compute","host":"cmp1"}]}`))
	})

	client := computeClient(fakeServer, "2.53")
	var buf bytes.Buffer
	err := runComputeServiceSet(context.Background(), client, "cmp1", "nova-compute", &serviceSetFlags{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to do") {
		t.Fatalf("err = %v, want nothing to do", err)
	}
}

func TestRunQuotaShow_ProjectAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-quota-sets/proj-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"quota_set":{"id":"proj-1","instances":10,"cores":20,"ram":51200,"key_pairs":100,"metadata_items":128,"server_groups":10,"server_group_members":10}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runQuotaShow(context.Background(), client, o, "proj-1", false, &buf); err != nil {
		t.Fatalf("runQuotaShow: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"Instances", "Cores", "RAM", "Key Pairs", "10", "20", "51200"} {
		if !strings.Contains(out, want) {
			t.Errorf("quota output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunQuotaShow_Defaults(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-quota-sets/proj-1/defaults", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"quota_set":{"id":"proj-1","instances":5,"cores":10,"ram":25600}}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runQuotaShow(context.Background(), client, o, "proj-1", true, &buf); err != nil {
		t.Fatalf("runQuotaShow (defaults): %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if !strings.Contains(buf.String(), "5") {
		t.Errorf("defaults output = %q, want instances=5", buf.String())
	}
}

func TestRunHypervisorList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hypervisors":[
			{"id":"1","hypervisor_hostname":"cmp1","hypervisor_type":"QEMU","hypervisor_version":2010000,"state":"up","status":"enabled"},
			{"id":"2","hypervisor_hostname":"cmp2","hypervisor_type":"QEMU","hypervisor_version":2010000,"state":"down","status":"disabled"}
		]}`))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runHypervisorList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runHypervisorList: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"Hypervisor Hostname", "cmp1", "cmp2", "QEMU", "up", "down", "enabled", "disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("hypervisor list output missing %q\n---\n%s", want, out)
		}
	}
}
