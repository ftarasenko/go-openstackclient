package baremetal

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// nodeGetBody renders a single-node GET response with the given states.
func nodeGetBody(provision, target, lastErr string) string {
	return fmt.Sprintf(`{
	  "uuid": "11111111-1111-1111-1111-111111111111",
	  "name": "node-a",
	  "provision_state": %q,
	  "target_provision_state": %q,
	  "last_error": %q
	}`, provision, target, lastErr)
}

// serveNodeGetSequence registers /nodes/{id} to return the supplied bodies in
// order, repeating the last one once exhausted.
func serveNodeGetSequence(fakeServer th.FakeServer, id string, bodies ...string) {
	var mu sync.Mutex
	i := 0
	fakeServer.Mux.HandleFunc("/nodes/"+id, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := bodies[i]
		if i < len(bodies)-1 {
			i++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

func TestWaitForProvisionState_WaitsThenSucceeds(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// Speed up polling for the test.
	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	// First poll: still transitioning (target set) even though provision_state
	// already equals want. Second poll: target cleared + want => success.
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("active", "rebuild", ""),
		nodeGetBody("active", "", ""),
	)

	client := baremetalClient(fakeServer, "latest")
	if err := waitForProvisionState(context.Background(), client, id, "active", time.Minute); err != nil {
		t.Fatalf("waitForProvisionState returned error: %v", err)
	}
}

func TestWaitForProvisionState_FailsOnUnexpectedSettledState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	// Verify failure: transition settles (target cleared) into "enroll" instead
	// of the wanted "manageable", with an error set.
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("verifying", "manageable", ""),
		nodeGetBody("enroll", "", "credentials verification failed"),
	)

	client := baremetalClient(fakeServer, "latest")
	err := waitForProvisionState(context.Background(), client, id, "manageable", time.Minute)
	if err == nil {
		t.Fatal("expected error for unexpected settled state, got nil")
	}
	if !strings.Contains(err.Error(), "credentials verification failed") {
		t.Errorf("error should include last_error, got: %v", err)
	}
}

// TestWaitForProvisionState_FailsOnUnexpectedSettledStateNoLastError verifies
// that a node which settles (target_provision_state cleared) into a state other
// than the wanted one is treated as a terminal failure even when last_error is
// empty, instead of hanging until the timeout expires.
func TestWaitForProvisionState_FailsOnUnexpectedSettledStateNoLastError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	// Settles into "available" instead of the wanted "manageable", with no
	// last_error. A generous timeout ensures the test fails (hangs) unless the
	// terminal-state detection returns promptly.
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("available", "", ""),
	)

	client := baremetalClient(fakeServer, "latest")
	err := waitForProvisionState(context.Background(), client, id, "manageable", time.Minute)
	if err == nil {
		t.Fatal("expected error for unexpected settled state, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected state") || !strings.Contains(err.Error(), "available") {
		t.Errorf("error should name the unexpected settled state, got: %v", err)
	}
}

func TestWaitForProvisionState_FailsOnFailureState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("deploy failed", "active", "deploy step failed"),
	)

	client := baremetalClient(fakeServer, "latest")
	err := waitForProvisionState(context.Background(), client, id, "active", time.Minute)
	if err == nil {
		t.Fatal("expected error for failure state, got nil")
	}
	if !strings.Contains(err.Error(), "deploy failed") {
		t.Errorf("error should name the failure state, got: %v", err)
	}
}

// TestWaitForProvisionSettled_TerminalFailureState covers the successful-abort
// path: ironic leaves provision_state="inspect failed" with
// target_provision_state="manageable" still populated, so keying only off
// target_provision_state clearing would spin until the timeout on SUCCESS.
func TestWaitForProvisionSettled_TerminalFailureState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("inspecting", "manageable", ""),
		nodeGetBody("inspect failed", "manageable", "Inspection was aborted by request."),
	)

	client := baremetalClient(fakeServer, "latest")
	// A generous timeout: the test hangs (and fails) unless the terminal-state
	// detection returns promptly.
	state, lastErr, err := waitForProvisionSettled(context.Background(), client, id, time.Minute)
	if err != nil {
		t.Fatalf("waitForProvisionSettled returned error: %v", err)
	}
	if state != "inspect failed" {
		t.Errorf("state = %q, want %q", state, "inspect failed")
	}
	if lastErr != "Inspection was aborted by request." {
		t.Errorf("lastError = %q, want the ironic last_error", lastErr)
	}
}

// TestWaitForProvisionSettled_TargetCleared covers the other exit: an abort that
// lands back on a normal stable state with target_provision_state cleared.
func TestWaitForProvisionSettled_TargetCleared(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("deleting", "available", ""),
		nodeGetBody("available", "", ""),
	)

	client := baremetalClient(fakeServer, "latest")
	state, lastErr, err := waitForProvisionSettled(context.Background(), client, id, time.Minute)
	if err != nil {
		t.Fatalf("waitForProvisionSettled returned error: %v", err)
	}
	if state != "available" {
		t.Errorf("state = %q, want %q", state, "available")
	}
	if lastErr != "" {
		t.Errorf("lastError = %q, want empty", lastErr)
	}
}

// TestRunNodeAbort_WaitReportsFailureStateAsSuccess drives the runXxx seam end
// to end: PUT the abort, poll, and render the settled state plus last_error
// without returning an error (exit 0).
func TestRunNodeAbort_WaitReportsFailureStateAsSuccess(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	fakeServer.Mux.HandleFunc("/nodes/"+id+"/states/provision", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "PUT")
		th.TestHeader(t, r, "X-OpenStack-Ironic-API-Version", "latest")
		th.TestJSONRequest(t, r, `{"target": "abort"}`)
		w.WriteHeader(http.StatusAccepted)
	})
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("inspect failed", "manageable", "Inspection was aborted by request."),
	)

	client := baremetalClient(fakeServer, "latest")
	var buf bytes.Buffer
	if err := runNodeAbort(context.Background(), client, id, true, time.Minute, &buf); err != nil {
		t.Fatalf("runNodeAbort returned error: %v", err)
	}
	want := "Node " + id + " settled in provision state \"inspect failed\"\n" +
		"Last error: Inspection was aborted by request.\n"
	if buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}
}
