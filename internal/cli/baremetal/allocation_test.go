package baremetal

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

const allocationID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func allocationBody(state, nodeUUID, lastError string) string {
	return `{"uuid": "` + allocationID + `", "name": "web-1", "state": "` + state + `",
	  "node_uuid": "` + nodeUUID + `", "resource_class": "baremetal.small",
	  "traits": ["CUSTOM_GPU"], "candidate_nodes": [], "last_error": "` + lastError + `",
	  "extra": {"owner": "team-a"}}`
}

func TestRunAllocationList_FiltersAndColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery, gotMicroversion string
	fakeServer.Mux.HandleFunc("/allocations", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotMicroversion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allocations": [` + allocationBody("active", "node-1", "") + `]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &allocationListFlags{resourceClass: "baremetal.small", state: "active"}
	client := baremetalClient(fakeServer, "latest")
	if err := runAllocationList(context.Background(), client, o, f, &out); err != nil {
		t.Fatalf("runAllocationList returned error: %v", err)
	}

	th.AssertEquals(t, "latest", gotMicroversion)
	for _, want := range []string{"resource_class=baremetal.small", "state=active"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	th.AssertEquals(t, allocationID+"\tweb-1\tbaremetal.small\tactive\tnode-1\n", out.String())
}

func TestRunAllocationCreate_BodyAndRequiredResourceClass(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/allocations", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "POST", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(allocationBody("allocating", "", "")))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &allocationCreateFlags{
		resourceClass:  "baremetal.small",
		name:           "web-1",
		traits:         []string{"CUSTOM_GPU"},
		candidateNodes: []string{"node-1", "node-2"},
		extra:          []string{"owner=team-a"},
	}
	client := baremetalClient(fakeServer, "latest")
	if err := runAllocationCreate(context.Background(), client, o, f, &out); err != nil {
		t.Fatalf("runAllocationCreate returned error: %v", err)
	}

	th.AssertEquals(t, "baremetal.small", body["resource_class"])
	th.AssertEquals(t, "web-1", body["name"])
	th.AssertDeepEquals(t, []any{"CUSTOM_GPU"}, body["traits"])
	th.AssertDeepEquals(t, []any{"node-1", "node-2"}, body["candidate_nodes"])
	th.AssertEquals(t, "team-a", body["extra"].(map[string]any)["owner"])
	// Without --wait the allocation is reported as ironic returned it, still
	// allocating and with no node attached.
	if !strings.Contains(out.String(), "allocating") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestRunAllocationCreate_WaitPollsUntilActive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { allocationPollInterval = prev }(allocationPollInterval)
	allocationPollInterval = time.Millisecond

	fakeServer.Mux.HandleFunc("/allocations", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(allocationBody("allocating", "", "")))
	})
	var mu sync.Mutex
	calls := 0
	fakeServer.Mux.HandleFunc("/allocations/"+allocationID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		state, node := "allocating", ""
		if calls > 1 {
			state, node = "active", "node-1"
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(allocationBody(state, node, "")))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &allocationCreateFlags{resourceClass: "baremetal.small", wait: true, waitTimeout: time.Minute}
	client := baremetalClient(fakeServer, "latest")
	if err := runAllocationCreate(context.Background(), client, o, f, &out); err != nil {
		t.Fatalf("runAllocationCreate returned error: %v", err)
	}
	// Creation is asynchronous: without --wait the node_uuid is empty, so the
	// wait is what makes the result useful.
	if !strings.Contains(out.String(), "node-1") {
		t.Errorf("--wait did not report the allocated node:\n%s", out.String())
	}
}

func TestWaitForAllocation_ErrorStateIsTerminal(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { allocationPollInterval = prev }(allocationPollInterval)
	allocationPollInterval = time.Millisecond

	fakeServer.Mux.HandleFunc("/allocations/"+allocationID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(allocationBody("error", "", "no suitable node found")))
	})

	client := baremetalClient(fakeServer, "latest")
	_, err := waitForAllocation(context.Background(), client, allocationID, time.Minute)
	if err == nil {
		t.Fatal("expected the error state to fail immediately")
	}
	// ironic records why in last_error; swallowing it would leave the operator
	// with nothing to act on.
	if !strings.Contains(err.Error(), "no suitable node found") {
		t.Errorf("error %q does not carry ironic's last_error", err)
	}
}

func TestRunAllocationSet_BuildsAJSONPatch(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var ops []map[string]any
	fakeServer.Mux.HandleFunc("/allocations/"+allocationID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.Method == http.MethodPatch {
			if err := json.NewDecoder(r.Body).Decode(&ops); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(allocationBody("active", "node-1", "")))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	err := runAllocationSet(context.Background(), client, o, allocationID, "renamed",
		[]string{"owner=team-b"}, &out)
	if err != nil {
		t.Fatalf("runAllocationSet returned error: %v", err)
	}

	th.AssertEquals(t, "PATCH", gotMethod)
	th.AssertEquals(t, 2, len(ops))
	th.AssertEquals(t, "replace", ops[0]["op"])
	th.AssertEquals(t, "/name", ops[0]["path"])
	th.AssertEquals(t, "renamed", ops[0]["value"])
	th.AssertEquals(t, "add", ops[1]["op"])
	th.AssertEquals(t, "/extra/owner", ops[1]["path"])
}

func TestRunAllocationUnset_RemoveOpsCarryNoValue(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var ops []map[string]any
	fakeServer.Mux.HandleFunc("/allocations/"+allocationID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			if err := json.NewDecoder(r.Body).Decode(&ops); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(allocationBody("active", "node-1", "")))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runAllocationUnset(context.Background(), client, o, allocationID, true, []string{"owner"}, &out); err != nil {
		t.Fatalf("runAllocationUnset returned error: %v", err)
	}

	th.AssertEquals(t, 2, len(ops))
	for _, op := range ops {
		th.AssertEquals(t, "remove", op["op"])
		// ironic rejects a remove operation that carries a value, so the field
		// has to be omitted rather than sent empty.
		if _, present := op["value"]; present {
			t.Errorf("remove operation carries a value: %#v", op)
		}
	}
	// A key containing '/' or '~' must be escaped per RFC 6901 before it is
	// appended to the pointer path.
	th.AssertEquals(t, "/extra/owner", ops[1]["path"])
}

func TestRunAllocationDelete_DeletesEach(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	fakeServer.Mux.HandleFunc("/allocations/", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "DELETE", r.Method)
		deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/allocations/"))
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runAllocationDelete(context.Background(), client, []string{allocationID, "second"}, &out); err != nil {
		t.Fatalf("runAllocationDelete returned error: %v", err)
	}
	th.AssertDeepEquals(t, []string{allocationID, "second"}, deleted)
}

// Ironic has carried `owner` on allocations since API 1.60 — below the 1.82 Zed
// cap, so it is present on every cloud koc supports — but gophercloud v2 models
// it nowhere. Upstream `openstack baremetal allocation show` prints it, so koc
// dropping it was a visible gap against OSC.
func TestRunAllocationShow_RendersOwner(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/allocations/"+allocationID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "GET", r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid": "` + allocationID + `", "name": "alloc-1", "state": "active",
		  "resource_class": "baremetal", "owner": "proj-42"}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runAllocationShow(context.Background(), client, o, allocationID, &out); err != nil {
		t.Fatalf("runAllocationShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), "proj-42") {
		t.Errorf("output is missing the owner:\n%s", out.String())
	}
}

func TestRunAllocationCreate_SendsOwner(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/allocations", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uuid": "` + allocationID + `", "state": "allocating", "owner": "proj-42"}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	f := &allocationCreateFlags{resourceClass: "baremetal", owner: "proj-42"}
	if err := runAllocationCreate(context.Background(), client, o, f, &out); err != nil {
		t.Fatalf("runAllocationCreate returned error: %v", err)
	}
	th.AssertEquals(t, "proj-42", body["owner"])
}

// The owner filter is a query parameter gophercloud's ListOpts has no field for.
func TestRunAllocationList_OwnerFilterReachesTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/allocations", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allocations": [{"uuid": "` + allocationID + `", "owner": "proj-42"}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	f := &allocationListFlags{owner: "proj-42", long: true}
	if err := runAllocationList(context.Background(), client, o, f, &out); err != nil {
		t.Fatalf("runAllocationList returned error: %v", err)
	}
	if !strings.Contains(gotQuery, "owner=proj-42") {
		t.Errorf("query %q is missing the owner filter", gotQuery)
	}
	if !strings.Contains(out.String(), "proj-42") {
		t.Errorf("--long output is missing the owner column:\n%s", out.String())
	}
}
