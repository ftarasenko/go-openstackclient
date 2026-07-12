package baremetal

import (
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
