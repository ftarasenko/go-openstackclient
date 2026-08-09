package placement

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

const providerUUID = "aaaaaaaa-1111-1111-1111-aaaaaaaaaaaa"

func TestParseInventory_RejectsUnknownFields(t *testing.T) {
	inv, err := parseInventory([]string{"total=64", "reserved=2", "allocation_ratio=1.5"})
	if err != nil {
		t.Fatalf("parseInventory returned error: %v", err)
	}
	th.AssertEquals(t, 64, inv.Total)
	th.AssertEquals(t, 2, inv.Reserved)
	th.AssertEquals(t, float32(1.5), inv.AllocationRatio)

	// Placement's inventory object is a closed set; a typo here would be a
	// silently ignored field otherwise.
	if _, err := parseInventory([]string{"totl=64"}); err == nil {
		t.Error("expected an unknown inventory field to be rejected")
	}
	if _, err := parseInventory([]string{"total=lots"}); err == nil {
		t.Error("expected a non-numeric total to be rejected")
	}
}

func TestParseAllocationSpec(t *testing.T) {
	provider, resources, err := parseAllocationSpec("rp=" + providerUUID + ",VCPU=2,MEMORY_MB=1024")
	if err != nil {
		t.Fatalf("parseAllocationSpec returned error: %v", err)
	}
	th.AssertEquals(t, providerUUID, provider)
	th.AssertEquals(t, 2, resources["VCPU"])
	th.AssertEquals(t, 1024, resources["MEMORY_MB"])

	if _, _, err := parseAllocationSpec("VCPU=2"); err == nil {
		t.Error("expected a spec with no rp= to be rejected")
	}
	if _, _, err := parseAllocationSpec("rp=" + providerUUID); err == nil {
		t.Error("expected a spec with no resources to be rejected")
	}
}

func TestRunProviderSet_EmptyParentReRootsTheProvider(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid": "` + providerUUID + `", "name": "compute-1", "generation": 3}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	// An explicitly empty --parent-provider makes it a root provider, which is
	// not the same as omitting the flag.
	err := runProviderSet(context.Background(), client, o, providerUUID, "", "", false, true, &out)
	if err != nil {
		t.Fatalf("runProviderSet returned error: %v", err)
	}
	// Placement un-parents a provider when parent_provider_uuid is null (1.37),
	// so the key has to be present and null — omitting it would mean "leave the
	// parent alone".
	got, present := body["parent_provider_uuid"]
	if !present || got != nil {
		t.Errorf("empty parent must be sent as an explicit null, got %#v", body)
	}
	if _, present := body["name"]; present {
		t.Errorf("name sent although --name was not given: %#v", body)
	}
}

func TestRunProviderInventorySet_ReadsGenerationFirst(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	var methods []string
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/inventories", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource_provider_generation": 7, "inventories": {
		  "VCPU": {"total": 16, "reserved": 0, "min_unit": 1, "max_unit": 16, "step_size": 1, "allocation_ratio": 16.0}
		}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	err := runProviderInventorySet(context.Background(), client, o, providerUUID,
		[]string{"VCPU:total=16", "VCPU:max_unit=16"}, &out)
	if err != nil {
		t.Fatalf("runProviderInventorySet returned error: %v", err)
	}

	// Placement rejects the write without a current generation, so the GET is
	// not optional.
	th.AssertDeepEquals(t, []string{"GET", "PUT"}, methods)
	th.AssertEquals(t, float64(7), body["resource_provider_generation"])
	vcpu := body["inventories"].(map[string]any)["VCPU"].(map[string]any)
	th.AssertEquals(t, float64(16), vcpu["total"])
	th.AssertEquals(t, float64(16), vcpu["max_unit"])
}

func TestRunProviderTraitSet_ReplacesTheWholeList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/traits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource_provider_generation": 4,
		  "traits": ["CUSTOM_GOLD", "HW_CPU_X86_AVX2"]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	err := runProviderTraitSet(context.Background(), client, o, providerUUID,
		[]string{"CUSTOM_GOLD", "HW_CPU_X86_AVX2"}, &out)
	if err != nil {
		t.Fatalf("runProviderTraitSet returned error: %v", err)
	}
	th.AssertEquals(t, float64(4), body["resource_provider_generation"])
	th.AssertDeepEquals(t, []any{"CUSTOM_GOLD", "HW_CPU_X86_AVX2"}, body["traits"])
}

func TestRunProviderAllocationUnset_KeepsTheOtherProviders(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const consumer = "cccccccc-1111-1111-1111-cccccccccccc"
	const otherProvider = "bbbbbbbb-2222-2222-2222-bbbbbbbbbbbb"
	var body map[string]any
	var methods []string
	fakeServer.Mux.HandleFunc("/allocations/"+consumer, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			// Placement answers a successful allocation write with 204.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allocations": {
		  "` + providerUUID + `":  {"generation": 1, "resources": {"VCPU": 2}},
		  "` + otherProvider + `": {"generation": 1, "resources": {"DISK_GB": 20}}
		}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	err := runProviderAllocationUnset(context.Background(), client, o, consumer,
		[]string{providerUUID}, nil, &out)
	if err != nil {
		t.Fatalf("runProviderAllocationUnset returned error: %v", err)
	}

	// Placement has no partial delete, so the survivors are written back.
	allocs := body["allocations"].(map[string]any)
	if _, present := allocs[providerUUID]; present {
		t.Errorf("the named provider was not dropped: %#v", allocs)
	}
	if _, present := allocs[otherProvider]; !present {
		t.Errorf("an unrelated provider's allocation was lost: %#v", allocs)
	}
	if methods[0] != "GET" {
		t.Errorf("the current allocations must be read before writing back: %v", methods)
	}
}

func TestRunProviderAllocationUnset_LastOneDeletesInstead(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const consumer = "cccccccc-1111-1111-1111-cccccccccccc"
	var methods []string
	fakeServer.Mux.HandleFunc("/allocations/"+consumer, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allocations": {
		  "` + providerUUID + `": {"generation": 1, "resources": {"VCPU": 2}}
		}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	err := runProviderAllocationUnset(context.Background(), client, o, consumer, []string{providerUUID}, nil, &out)
	if err != nil {
		t.Fatalf("runProviderAllocationUnset returned error: %v", err)
	}
	// Placement rejects a PUT carrying an empty allocations object, so clearing
	// the last one has to be a DELETE.
	if methods[len(methods)-1] != "DELETE" {
		t.Errorf("clearing the last allocation must DELETE, got %v", methods)
	}
}

func TestRunAllocationCandidateList_JoinsResourcesIntoOneParameter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/allocation_candidates", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allocation_requests": [], "provider_summaries": {
		  "` + providerUUID + `": {"resources": {"VCPU": {"capacity": 64, "used": 4}}}
		}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	err := runAllocationCandidateList(context.Background(), client, o,
		[]string{"VCPU=2", "MEMORY_MB=1024"}, nil, nil, 0, &out)
	if err != nil {
		t.Fatalf("runAllocationCandidateList returned error: %v", err)
	}
	// Placement takes one comma-separated `resources` parameter, not repeated
	// ones — repeating it would make the later value win and silently drop the
	// rest of the request.
	if !strings.Contains(gotQuery, "resources=VCPU%3A2%2CMEMORY_MB%3A1024") &&
		!strings.Contains(gotQuery, "resources=VCPU%3D2%2CMEMORY_MB%3D1024") {
		t.Errorf("query %q does not carry one joined resources parameter", gotQuery)
	}
	if !strings.Contains(out.String(), "VCPU") {
		t.Errorf("output is missing the provider summary:\n%s", out.String())
	}
}

func TestRunTraitShow_ExistenceOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/traits/CUSTOM_GOLD", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "GET", r.Method)
		// Placement answers 204 with no body; the name has to be echoed.
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	if err := runTraitShow(context.Background(), client, o, "CUSTOM_GOLD", &out); err != nil {
		t.Fatalf("runTraitShow returned error: %v", err)
	}
	th.AssertEquals(t, "CUSTOM_GOLD\n", out.String())
}

func TestRunResourceClassSet_IsAnIdempotentCreate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/resource_classes/CUSTOM_FPGA", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		// Placement answers the resource-class PUT with 201 when it created the
		// class and 204 when it already existed.
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := placementClient(fakeServer, "latest")
	if err := runResourceClassSet(context.Background(), client, o, "CUSTOM_FPGA", &out); err != nil {
		t.Fatalf("runResourceClassSet returned error: %v", err)
	}
	// osc-placement's `resource class set` is a PUT that creates when missing,
	// not a rename.
	th.AssertEquals(t, "PUT", gotMethod)
	th.AssertEquals(t, "CUSTOM_FPGA\n", out.String())
}

func TestSortRowsByFirstColumn(t *testing.T) {
	rows := [][]any{{"b", "2"}, {"a", "1"}, {"c", "3"}}
	sortRowsByFirstColumn(rows)
	// Several of these results come from maps, whose iteration order would
	// otherwise vary between runs.
	th.AssertEquals(t, "a", rows[0][0])
	th.AssertEquals(t, "b", rows[1][0])
	th.AssertEquals(t, "c", rows[2][0])
}
