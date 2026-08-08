package server

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

const serverGroupID = "66666666-6666-6666-6666-666666666666"

func TestRunServerGroupList_AllProjectsAndPolicyColumn(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		// A 2.64+ cloud reports `policy`; an older one a one-element `policies`
		// array. Both have to render.
		_, _ = w.Write([]byte(`{"server_groups": [
		  {"id": "` + serverGroupID + `", "name": "web", "policy": "anti-affinity", "members": []},
		  {"id": "77777777-7777-7777-7777-777777777777", "name": "legacy", "policies": ["affinity"], "members": []}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := computeClient(fakeServer, "latest")
	if err := runServerGroupList(context.Background(), client, o, true, false, 0, &out); err != nil {
		t.Fatalf("runServerGroupList returned error: %v", err)
	}

	th.AssertEquals(t, "all_projects=true", gotQuery)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	th.AssertEquals(t, 2, len(lines))
	if !strings.Contains(lines[0], "anti-affinity") {
		t.Errorf("the 2.64 `policy` field did not render: %q", lines[0])
	}
	if !strings.Contains(lines[1], "affinity") {
		t.Errorf("the pre-2.64 `policies` array did not render: %q", lines[1])
	}
}

func TestRunServerGroupCreate_UsesPolicyAt264(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "POST", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_group": {"id": "` + serverGroupID + `", "name": "web",
		  "policy": "anti-affinity", "rules": {"max_server_per_host": 2}, "members": []}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := computeClient(fakeServer, "latest")
	err := runServerGroupCreate(context.Background(), client, o, "web", "anti-affinity",
		[]string{"max_server_per_host=2"}, &out)
	if err != nil {
		t.Fatalf("runServerGroupCreate returned error: %v", err)
	}

	group := body["server_group"].(map[string]any)
	th.AssertEquals(t, "anti-affinity", group["policy"])
	th.AssertEquals(t, float64(2), group["rules"].(map[string]any)["max_server_per_host"])
	// nova's create_v264 schema deletes `policies` and sets
	// additionalProperties=false, so sending it alongside would be a 400.
	if _, present := group["policies"]; present {
		t.Errorf("`policies` sent to a 2.64+ cloud, which rejects it: %#v", group)
	}
}

func TestRunServerGroupCreate_FallsBackToPoliciesBelow264(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_group": {"id": "` + serverGroupID + `", "name": "web",
		  "policies": ["affinity"], "members": []}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	// Pinned below 2.64: nova's older schema knows only the array form and
	// rejects `policy` outright.
	client := computeClient(fakeServer, "2.60")
	if err := runServerGroupCreate(context.Background(), client, o, "web", "affinity", nil, &out); err != nil {
		t.Fatalf("runServerGroupCreate returned error: %v", err)
	}

	group := body["server_group"].(map[string]any)
	th.AssertDeepEquals(t, []any{"affinity"}, group["policies"])
	if _, present := group["policy"]; present {
		t.Errorf("`policy` sent to a pre-2.64 cloud, which rejects it: %#v", group)
	}
}

func TestRunServerGroupCreate_RuleBelow264IsRejectedLocally(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := computeClient(fakeServer, "2.60")
	err := runServerGroupCreate(context.Background(), client, o, "web", "anti-affinity",
		[]string{"max_server_per_host=2"}, &out)
	if err == nil {
		t.Fatal("expected --rule to be rejected when pinned below 2.64")
	}
	if !strings.Contains(err.Error(), "2.64") {
		t.Errorf("error %q does not name the required microversion", err)
	}
}

func TestParseServerGroupRules(t *testing.T) {
	r, err := parseServerGroupRules([]string{"max_server_per_host=3"})
	if err != nil {
		t.Fatalf("parseServerGroupRules returned error: %v", err)
	}
	th.AssertEquals(t, 3, r.MaxServerPerHost)

	if _, err := parseServerGroupRules([]string{"max_server_per_host=many"}); err == nil {
		t.Error("expected a non-numeric rule value to be rejected")
	}
	// nova defines exactly one rule; an unknown key is a typo worth catching
	// before the round trip.
	if _, err := parseServerGroupRules([]string{"max_servers=3"}); err == nil {
		t.Error("expected an unknown rule key to be rejected")
	}
}

func TestResolveServerGroupID_AmbiguousNameFails(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-server-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server_groups": [
		  {"id": "` + serverGroupID + `", "name": "web"},
		  {"id": "77777777-7777-7777-7777-777777777777", "name": "web"}
		]}`))
	})

	client := computeClient(fakeServer, "latest")
	// Server group names are not unique in nova, so picking one silently would
	// act on an arbitrary group.
	if _, err := resolveServerGroupID(context.Background(), client, "web"); err == nil {
		t.Fatal("expected an ambiguous name to be rejected")
	}
}

func TestRunServerGroupDelete_ResolvesThenDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/os-server-groups/"+serverGroupID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := computeClient(fakeServer, "latest")
	if err := runServerGroupDelete(context.Background(), client, []string{serverGroupID}); err != nil {
		t.Fatalf("runServerGroupDelete returned error: %v", err)
	}
	th.AssertEquals(t, "DELETE", gotMethod)
	th.AssertEquals(t, "/os-server-groups/"+serverGroupID, gotPath)
}
