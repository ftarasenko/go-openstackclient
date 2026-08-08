package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// captureProvisionRequest registers the provision endpoint for one node and
// records the method, microversion header and decoded body of the request.
func captureProvisionRequest(t *testing.T, fakeServer th.FakeServer, id string, status int) (*string, *string, *map[string]any) {
	t.Helper()
	var method, microversion string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/nodes/"+id+"/states/provision", func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		microversion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(status)
	})
	return &method, &microversion, &body
}

func TestRunNodeClean_InlineJSONSteps(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	method, microversion, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)

	f := &stepsFlags{steps: `[{"interface": "deploy", "step": "erase_devices", "args": {"tags": ["fast"]}}]`}
	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeClean(context.Background(), client, id, f, false, 0, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runNodeClean returned error: %v", err)
	}

	th.AssertEquals(t, "PUT", *method)
	th.AssertEquals(t, "latest", *microversion)
	th.AssertEquals(t, "clean", (*body)["target"])

	steps, ok := (*body)["clean_steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("clean_steps = %#v, want one step", (*body)["clean_steps"])
	}
	step := steps[0].(map[string]any)
	th.AssertEquals(t, "deploy", step["interface"])
	th.AssertEquals(t, "erase_devices", step["step"])
	// args is driver-defined, so it must survive the round trip untouched.
	args, ok := step["args"].(map[string]any)
	if !ok {
		t.Fatalf("step args = %#v, want an object", step["args"])
	}
	tags, ok := args["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != "fast" {
		t.Fatalf("step args tags = %#v, want [fast]", args["tags"])
	}
	th.AssertEquals(t, "Requested clean for node "+id+"\n", out.String())
}

func TestRunNodeClean_YAMLStepsFromFileWithExtras(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	_, _, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)

	// Upstream documents the steps argument as "a YAML file OR a JSON string",
	// so a YAML file has to work.
	path := filepath.Join(t.TempDir(), "steps.yaml")
	yamlSteps := "- interface: raid\n  step: create_configuration\n  args:\n    create_root_volume: true\n"
	if err := os.WriteFile(path, []byte(yamlSteps), 0o600); err != nil {
		t.Fatalf("writing steps file: %v", err)
	}

	f := &stepsFlags{steps: path, disableRamdisk: true}
	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeClean(context.Background(), client, id, f, false, 0, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runNodeClean returned error: %v", err)
	}

	th.AssertEquals(t, true, (*body)["disable_ramdisk"])
	steps := (*body)["clean_steps"].([]any)
	step := steps[0].(map[string]any)
	th.AssertEquals(t, "raid", step["interface"])
	th.AssertEquals(t, "create_configuration", step["step"])
	args := step["args"].(map[string]any)
	th.AssertEquals(t, true, args["create_root_volume"])
}

func TestRunNodeClean_StepsFromStdin(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	_, _, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)

	f := &stepsFlags{steps: "-"}
	stdin := strings.NewReader(`[{"interface": "deploy", "step": "erase_devices_metadata"}]`)
	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeClean(context.Background(), client, id, f, false, 0, stdin, &out); err != nil {
		t.Fatalf("runNodeClean returned error: %v", err)
	}

	steps := (*body)["clean_steps"].([]any)
	th.AssertEquals(t, "erase_devices_metadata", steps[0].(map[string]any)["step"])
}

func TestRunNodeClean_RunbookInsteadOfSteps(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	_, _, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)

	f := &stepsFlags{runbook: "CUSTOM_AGGRESSIVE_CLEANING"}
	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeClean(context.Background(), client, id, f, false, 0, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runNodeClean returned error: %v", err)
	}

	th.AssertEquals(t, "CUSTOM_AGGRESSIVE_CLEANING", (*body)["runbook"])
	if _, present := (*body)["clean_steps"]; present {
		t.Fatalf("clean_steps must be absent when --runbook is used, body = %#v", *body)
	}
}

func TestParseSteps_RejectsIncompleteStep(t *testing.T) {
	if _, err := parseSteps([]byte(`[{"interface": "deploy"}]`)); err == nil {
		t.Fatal("expected an error for a step missing the \"step\" key")
	}
	if _, err := parseSteps([]byte(`[]`)); err == nil {
		t.Fatal("expected an error for an empty steps document")
	}
}

func TestRunNodeRescue_SendsPassword(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	method, _, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeRescue(context.Background(), client, id, "s3cret", false, 0, &out); err != nil {
		t.Fatalf("runNodeRescue returned error: %v", err)
	}

	th.AssertEquals(t, "PUT", *method)
	th.AssertEquals(t, "rescue", (*body)["target"])
	th.AssertEquals(t, "s3cret", (*body)["rescue_password"])
	th.AssertEquals(t, "Requested rescue for node "+id+"\n", out.String())
}

func TestRunNodeRescue_WaitReachesRescue(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("rescuing", "rescue", ""),
		nodeGetBody("rescue", "", ""),
	)

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeRescue(context.Background(), client, id, "s3cret", true, time.Minute, &out); err != nil {
		t.Fatalf("runNodeRescue returned error: %v", err)
	}
	th.AssertEquals(t, "Node "+id+" reached provision state \"rescue\"\n", out.String())
}

func TestRunNodeService_ExplainsMicroversionOnOldCloud(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	// A Zed cloud negotiates down to 1.82 and then rejects target=service as an
	// invalid value — a 400 that says nothing about microversions.
	fakeServer.Mux.HandleFunc("/nodes/"+id+"/states/provision", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ironicMaxVersionHeader, "1.82")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_message": "Invalid target"}`))
	})

	f := &stepsFlags{steps: `[{"interface": "deploy", "step": "reset"}]`}
	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	err := runNodeService(context.Background(), client, id, f, false, 0, strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"requires ironic API 1.87", "OpenStack 2023.2", "supports up to 1.82"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRunNodeService_KeepsErrorOnNewCloud(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	// A cloud that supports 1.87 rejecting the request means something else is
	// wrong; the microversion explanation must not be bolted on.
	fakeServer.Mux.HandleFunc("/nodes/"+id+"/states/provision", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ironicMaxVersionHeader, "1.95")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_message": "node is not active"}`))
	})

	f := &stepsFlags{steps: `[{"interface": "deploy", "step": "reset"}]`}
	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	err := runNodeService(context.Background(), client, id, f, false, 0, strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "requires ironic API") {
		t.Errorf("error %q should not claim a microversion problem on a 1.95 cloud", err)
	}
}

func TestRunNodeUnhold_SettlesAndReportsState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisionPollInterval = prev }(provisionPollInterval)
	provisionPollInterval = time.Millisecond

	const id = "11111111-1111-1111-1111-111111111111"
	_, _, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)
	serveNodeGetSequence(fakeServer, id,
		nodeGetBody("cleaning", "manageable", ""),
		nodeGetBody("manageable", "", ""),
	)

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeUnhold(context.Background(), client, id, true, time.Minute, &out); err != nil {
		t.Fatalf("runNodeUnhold returned error: %v", err)
	}
	th.AssertEquals(t, "unhold", (*body)["target"])
	th.AssertEquals(t, "Node "+id+" settled in provision state \"manageable\"\n", out.String())
}

func TestRunNodeAdopt_UsesAdoptTarget(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	method, _, body := captureProvisionRequest(t, fakeServer, id, http.StatusAccepted)

	var adopt provisionTransition
	for _, tr := range provisionTransitions() {
		if tr.verb == "adopt" {
			adopt = tr
		}
	}
	th.AssertEquals(t, "adopt", adopt.verb)

	var out bytes.Buffer
	client := baremetalClient(fakeServer, "latest")
	if err := runNodeProvision(context.Background(), client, adopt, id, false, 0, &out); err != nil {
		t.Fatalf("runNodeProvision returned error: %v", err)
	}
	th.AssertEquals(t, "PUT", *method)
	th.AssertEquals(t, "adopt", (*body)["target"])
}
