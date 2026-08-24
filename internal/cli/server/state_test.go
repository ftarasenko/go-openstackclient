package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const stateServerID = "11111111-1111-1111-1111-111111111111"

// captureServerAction registers the server action endpoint and records the
// decoded request bodies, in order.
func captureServerAction(t *testing.T, fakeServer th.FakeServer, id string) *[]map[string]any {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	return &bodies
}

// serveServerStatuses returns the given statuses in order from GET /servers/{id},
// repeating the last one once exhausted.
func serveServerStatuses(fakeServer th.FakeServer, id string, statuses ...string) {
	var mu sync.Mutex
	i := 0
	fakeServer.Mux.HandleFunc("/servers/"+id, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		status := statuses[i]
		if i < len(statuses)-1 {
			i++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server": {"id": "` + id + `", "name": "web-1", "status": "` + status + `"}}`))
	})
}

func TestRunServerShelve_PlainShelve(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bodies := captureServerAction(t, fakeServer, stateServerID)

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	if err := runServerShelve(context.Background(), client, []string{stateServerID}, false, false, 0, &out); err != nil {
		t.Fatalf("runServerShelve returned error: %v", err)
	}

	th.AssertEquals(t, 1, len(*bodies))
	if _, ok := (*bodies)[0]["shelve"]; !ok {
		t.Errorf("expected a shelve action, got %#v", (*bodies)[0])
	}
	th.AssertEquals(t, "Shelved server "+stateServerID+"\n", out.String())
}

func TestRunServerShelve_OffloadUsesDifferentActionAndRestState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bodies := captureServerAction(t, fakeServer, stateServerID)
	// Without --offload the server rests at SHELVED; with it, at
	// SHELVED_OFFLOADED. Waiting for the wrong one would hang until the timeout.
	serveServerStatuses(fakeServer, stateServerID, "SHELVING", "SHELVED_OFFLOADED")

	defer func(prev time.Duration) { statusPollInterval = prev }(statusPollInterval)
	statusPollInterval = time.Millisecond

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	err := runServerShelve(context.Background(), client, []string{stateServerID}, true, true, time.Minute, &out)
	if err != nil {
		t.Fatalf("runServerShelve returned error: %v", err)
	}
	if _, ok := (*bodies)[0]["shelveOffload"]; !ok {
		t.Errorf("expected a shelveOffload action, got %#v", (*bodies)[0])
	}
}

// With shelved_offload_time=0 nova goes SHELVING → SHELVED_OFFLOADED, and the
// intermediate SHELVED can pass between two polls. A plain shelve --wait that
// insisted on exactly "SHELVED" then span until --wait-timeout waiting for a
// status the server had already left.
func TestRunServerShelve_WaitAcceptsImmediateOffload(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	captureServerAction(t, fakeServer, stateServerID)
	serveServerStatuses(fakeServer, stateServerID, "SHELVING", "SHELVED_OFFLOADED")

	defer func(prev time.Duration) { statusPollInterval = prev }(statusPollInterval)
	statusPollInterval = time.Millisecond

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	// offload=false — the operator did not ask for it; nova did it anyway.
	err := runServerShelve(context.Background(), client, []string{stateServerID}, false, true, 2*time.Second, &out)
	if err != nil {
		t.Fatalf("runServerShelve returned error: %v", err)
	}
	th.AssertEquals(t, "Shelved server "+stateServerID+"\n", out.String())
}

func TestRunServerShelve_WaitFailsOnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	captureServerAction(t, fakeServer, stateServerID)
	serveServerStatuses(fakeServer, stateServerID, "SHELVING", "ERROR")

	defer func(prev time.Duration) { statusPollInterval = prev }(statusPollInterval)
	statusPollInterval = time.Millisecond

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	err := runServerShelve(context.Background(), client, []string{stateServerID}, false, true, time.Minute, &out)
	if err == nil {
		t.Fatal("expected an error when the server enters ERROR")
	}
	if !strings.Contains(err.Error(), "ERROR") {
		t.Errorf("error %q does not name the ERROR status", err)
	}
}

func TestRunServerUnshelve_AvailabilityZoneAndHost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bodies := captureServerAction(t, fakeServer, stateServerID)

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	err := runServerUnshelve(context.Background(), client, []string{stateServerID},
		&unshelveFlags{az: "az-1", host: "compute-3"}, &out)
	if err != nil {
		t.Fatalf("runServerUnshelve returned error: %v", err)
	}

	unshelve, ok := (*bodies)[0]["unshelve"].(map[string]any)
	if !ok {
		t.Fatalf("expected an unshelve object, got %#v", (*bodies)[0])
	}
	th.AssertEquals(t, "az-1", unshelve["availability_zone"])
	// host is not modelled by gophercloud's UnshelveOpts; it has to survive the
	// builder anyway.
	th.AssertEquals(t, "compute-3", unshelve["host"])
}

func TestRunServerUnshelve_HostAloneStillBuildsAnObject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bodies := captureServerAction(t, fakeServer, stateServerID)

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	// With no availability zone gophercloud renders {"unshelve": null}, so the
	// inner object has to be created before host can be added.
	err := runServerUnshelve(context.Background(), client, []string{stateServerID},
		&unshelveFlags{host: "compute-3"}, &out)
	if err != nil {
		t.Fatalf("runServerUnshelve returned error: %v", err)
	}
	unshelve, ok := (*bodies)[0]["unshelve"].(map[string]any)
	if !ok {
		t.Fatalf("expected an unshelve object, got %#v", (*bodies)[0])
	}
	th.AssertEquals(t, "compute-3", unshelve["host"])
}

func TestRunServerUnshelve_NoFlagsSendsNullBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bodies := captureServerAction(t, fakeServer, stateServerID)

	var out bytes.Buffer
	client := computeClient(fakeServer, "latest")
	if err := runServerUnshelve(context.Background(), client, []string{stateServerID},
		&unshelveFlags{}, &out); err != nil {
		t.Fatalf("runServerUnshelve returned error: %v", err)
	}
	// Nova below 2.77 rejects an unshelve body that is anything but null, so an
	// empty object here would break the older clouds koc supports.
	if got := (*bodies)[0]["unshelve"]; got != nil {
		t.Errorf("unshelve with no flags must send null, got %#v", got)
	}
}

func TestParseStringMap(t *testing.T) {
	m, err := parseStringMap([]string{"os_type=linux", "note=has=equals"})
	if err != nil {
		t.Fatalf("parseStringMap returned error: %v", err)
	}
	th.AssertEquals(t, "linux", m["os_type"])
	// Only the first '=' separates; the value may contain more.
	th.AssertEquals(t, "has=equals", m["note"])

	if _, err := parseStringMap([]string{"novalue"}); err == nil {
		t.Error("expected an error for a value with no '='")
	}
	if _, err := parseStringMap([]string{"=v"}); err == nil {
		t.Error("expected an error for an empty key")
	}
}

func TestWaitForServerStatus_TimesOutWithLastStatus(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServerStatuses(fakeServer, stateServerID, "SHELVING")

	defer func(prev time.Duration) { statusPollInterval = prev }(statusPollInterval)
	statusPollInterval = time.Millisecond

	client := computeClient(fakeServer, "latest")
	err := waitForServerStatus(context.Background(), client, stateServerID, "SHELVED", 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	// The status last seen is the difference between "nova is slow" and "koc
	// waited for the wrong state".
	if !strings.Contains(err.Error(), "SHELVING") {
		t.Errorf("timeout error %q does not report the last status seen", err)
	}
}

func TestRunServerImageCreate_DefaultsNameToServerName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+stateServerID+"/action", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		// From nova 2.45 the new image ID comes back in the response body; koc
		// negotiates "latest", so this is the live path on every supported cloud.
		w.Header().Set("X-OpenStack-Nova-API-Version", "2.93")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"image_id": "image-id-1"}`))
	})
	serveServerStatuses(fakeServer, stateServerID, "ACTIVE")

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := computeClient(fakeServer, "latest")
	err := runServerImageCreate(context.Background(), client, nil, o, stateServerID,
		&serverImageCreateFlags{properties: []string{"os_type=linux"}}, &out)
	if err != nil {
		t.Fatalf("runServerImageCreate returned error: %v", err)
	}

	snapshot := body["createImage"].(map[string]any)
	// The fixture's server is named web-1, and an omitted --name takes it.
	th.AssertEquals(t, "web-1", snapshot["name"])
	metadata := snapshot["metadata"].(map[string]any)
	th.AssertEquals(t, "linux", metadata["os_type"])
	if !strings.Contains(out.String(), "image-id-1") {
		t.Errorf("output missing the new image ID:\n%s", out.String())
	}
}

func TestRunServerImageCreate_ExplicitNameSkipsTheServerFetch(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+stateServerID+"/action", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		// Below 2.45 nova put the ID in the Location header instead. Both shapes
		// have to work, since --os-compute-api-version can pin an older one.
		w.Header().Set("X-OpenStack-Nova-API-Version", "2.44")
		w.Header().Set("Location", "http://example.com/v2/images/image-id-2")
		w.WriteHeader(http.StatusAccepted)
	})
	// No GET /servers/{id} handler is registered: with --name given, the server
	// must not be fetched at all.

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := computeClient(fakeServer, "latest")
	err := runServerImageCreate(context.Background(), client, nil, o, stateServerID,
		&serverImageCreateFlags{name: "nightly"}, &out)
	if err != nil {
		t.Fatalf("runServerImageCreate returned error: %v", err)
	}
	th.AssertEquals(t, "nightly", body["createImage"].(map[string]any)["name"])
	if !strings.Contains(out.String(), "image-id-2") {
		t.Errorf("the pre-2.45 Location header was not read:\n%s", out.String())
	}
}
