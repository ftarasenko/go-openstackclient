package placement

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --- resource class list / show / create / delete ---------------------------

const resourceClassListBody = `{
  "resource_classes": [
    {"name": "VCPU"},
    {"name": "CUSTOM_FPGA"}
  ]
}`

func TestRunResourceClassList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/resource_classes", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resourceClassListBody))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runResourceClassList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runResourceClassList returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"Name", "VCPU", "CUSTOM_FPGA"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunResourceClassList_PropagatesListError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_classes", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runResourceClassList(context.Background(), client, o, &buf); err == nil {
		t.Fatal("runResourceClassList = nil, want an error on a 500")
	}
}

func TestRunResourceClassShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_classes/CUSTOM_FPGA", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name": "CUSTOM_FPGA"}`))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runResourceClassShow(context.Background(), client, o, "CUSTOM_FPGA", &buf); err != nil {
		t.Fatalf("runResourceClassShow returned error: %v", err)
	}
	th.AssertEquals(t, "CUSTOM_FPGA\n", buf.String())
}

func TestRunResourceClassShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_classes/CUSTOM_MISSING", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runResourceClassShow(context.Background(), client, o, "CUSTOM_MISSING", &buf)
	if err == nil {
		t.Fatal("runResourceClassShow = nil, want an error on a 404")
	}
	if !strings.Contains(err.Error(), "CUSTOM_MISSING") {
		t.Errorf("error should name the class: %v", err)
	}
}

func TestRunResourceClassCreate_SendsTheNameAndSucceedsOnAnEmptyBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/resource_classes", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		// Placement answers a resource-class create with 201 and no body.
		w.WriteHeader(http.StatusCreated)
	})

	client := placementClient(fakeServer, "latest")
	if err := runResourceClassCreate(context.Background(), client, "CUSTOM_FPGA"); err != nil {
		t.Fatalf("runResourceClassCreate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	th.AssertEquals(t, "CUSTOM_FPGA", body["name"])
}

func TestRunResourceClassCreate_PropagatesConflict(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_classes", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	client := placementClient(fakeServer, "latest")
	err := runResourceClassCreate(context.Background(), client, "CUSTOM_FPGA")
	if err == nil {
		t.Fatal("runResourceClassCreate = nil, want an error on a 409")
	}
	if !strings.Contains(err.Error(), "CUSTOM_FPGA") {
		t.Errorf("error should name the class: %v", err)
	}
}

func TestRunResourceClassDelete_Request(t *testing.T) {
	tests := []struct {
		name    string
		classes []string
	}{
		{"single", []string{"CUSTOM_FPGA"}},
		{"multiple", []string{"CUSTOM_FPGA", "CUSTOM_GPU"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			gotMethod := map[string]string{}
			for _, class := range tc.classes {
				fakeServer.Mux.HandleFunc("/resource_classes/"+class, func(w http.ResponseWriter, r *http.Request) {
					gotMethod[class] = r.Method
					w.WriteHeader(http.StatusNoContent)
				})
			}

			client := placementClient(fakeServer, "latest")
			if err := runResourceClassDelete(context.Background(), client, tc.classes); err != nil {
				t.Fatalf("runResourceClassDelete returned error: %v", err)
			}
			for _, class := range tc.classes {
				if gotMethod[class] != http.MethodDelete {
					t.Errorf("request method for %s = %q, want DELETE", class, gotMethod[class])
				}
			}
		})
	}
}

// runResourceClassDelete goes through batchdelete.Each, so a failing class
// must not stop the rest of the batch from being attempted.
func TestRunResourceClassDelete_CollectsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bad, good := "CUSTOM_MISSING", "CUSTOM_FPGA"
	var attempted []string
	fakeServer.Mux.HandleFunc("/resource_classes/"+bad, func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, bad)
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/resource_classes/"+good, func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, good)
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "latest")
	err := runResourceClassDelete(context.Background(), client, []string{bad, good})
	if err == nil {
		t.Fatal("runResourceClassDelete = nil, want an error naming the missing class")
	}
	if len(attempted) != 2 {
		t.Errorf("attempted deletes = %v, want both classes attempted", attempted)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error missing failed class %s: %v", bad, err)
	}
}

// --- trait create / delete ---------------------------------------------------

func TestRunTraitCreate_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/traits/CUSTOM_GOLD", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		// Placement answers 201 when the trait is new, 204 when it already existed.
		w.WriteHeader(http.StatusCreated)
	})

	client := placementClient(fakeServer, "latest")
	if err := runTraitCreate(context.Background(), client, "CUSTOM_GOLD"); err != nil {
		t.Fatalf("runTraitCreate returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
}

func TestRunTraitCreate_PropagatesError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/traits/CUSTOM_BAD", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	client := placementClient(fakeServer, "latest")
	err := runTraitCreate(context.Background(), client, "CUSTOM_BAD")
	if err == nil {
		t.Fatal("runTraitCreate = nil, want an error on a 400")
	}
	if !strings.Contains(err.Error(), "CUSTOM_BAD") {
		t.Errorf("error should name the trait: %v", err)
	}
}

func TestRunTraitDelete_CollectsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bad, good := "CUSTOM_MISSING", "CUSTOM_GOLD"
	var attempted []string
	fakeServer.Mux.HandleFunc("/traits/"+bad, func(w http.ResponseWriter, r *http.Request) {
		attempted = append(attempted, bad)
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/traits/"+good, func(w http.ResponseWriter, r *http.Request) {
		attempted = append(attempted, good)
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "latest")
	err := runTraitDelete(context.Background(), client, []string{bad, good})
	if err == nil {
		t.Fatal("runTraitDelete = nil, want an error naming the missing trait")
	}
	if len(attempted) != 2 {
		t.Errorf("attempted deletes = %v, want both traits attempted", attempted)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error missing failed trait %s: %v", bad, err)
	}
}

// --- resource provider create -------------------------------------------------

func TestRunProviderCreate_SendsOnlyName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"uuid": "` + providerUUID + `", "name": "compute-1", "generation": 0}`))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runProviderCreate(context.Background(), client, o, "compute-1", "", "", &buf); err != nil {
		t.Fatalf("runProviderCreate returned error: %v", err)
	}
	th.AssertEquals(t, "compute-1", body["name"])
	// uuid and parent_provider_uuid are `omitempty` on the wire; sending them
	// as empty strings would ask placement to create a provider parented under
	// the empty string rather than leaving the fields unset.
	if _, present := body["uuid"]; present {
		t.Errorf("uuid sent although it was not given: %#v", body)
	}
	if _, present := body["parent_provider_uuid"]; present {
		t.Errorf("parent_provider_uuid sent although it was not given: %#v", body)
	}
	if !strings.Contains(buf.String(), providerUUID) {
		t.Errorf("output missing the created provider's uuid:\n%s", buf.String())
	}
}

func TestRunProviderCreate_SendsUUIDAndParentWhenGiven(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	parent := "bbbbbbbb-2222-2222-2222-bbbbbbbbbbbb"
	var body map[string]any
	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"uuid": "` + providerUUID + `", "name": "compute-1", "generation": 0,
		  "parent_provider_uuid": "` + parent + `"}`))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runProviderCreate(context.Background(), client, o, "compute-1", providerUUID, parent, &buf)
	if err != nil {
		t.Fatalf("runProviderCreate returned error: %v", err)
	}
	th.AssertEquals(t, providerUUID, body["uuid"])
	th.AssertEquals(t, parent, body["parent_provider_uuid"])
}

func TestRunProviderCreate_PropagatesError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runProviderCreate(context.Background(), client, o, "compute-1", "", "", &buf)
	if err == nil {
		t.Fatal("runProviderCreate = nil, want an error on a 409")
	}
	if !strings.Contains(err.Error(), "compute-1") {
		t.Errorf("error should name the provider: %v", err)
	}
}

// --- provider inventory delete -----------------------------------------------

func TestRunProviderInventoryDelete_OneClass(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/inventories/VCPU", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "latest")
	if err := runProviderInventoryDelete(context.Background(), client, providerUUID, "VCPU"); err != nil {
		t.Fatalf("runProviderInventoryDelete returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/resource_providers/"+providerUUID+"/inventories/VCPU" {
		t.Errorf("request path = %q, want the class-scoped inventory URL", gotPath)
	}
}

func TestRunProviderInventoryDelete_EveryClassWhenNoneNamed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/inventories", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "latest")
	if err := runProviderInventoryDelete(context.Background(), client, providerUUID, ""); err != nil {
		t.Fatalf("runProviderInventoryDelete returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/resource_providers/"+providerUUID+"/inventories" {
		t.Errorf("request path = %q, want the whole-provider inventory URL", gotPath)
	}
}

func TestRunProviderInventoryDelete_PropagatesError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/inventories/VCPU", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := placementClient(fakeServer, "latest")
	err := runProviderInventoryDelete(context.Background(), client, providerUUID, "VCPU")
	if err == nil {
		t.Fatal("runProviderInventoryDelete = nil, want an error on a 404")
	}
	if !strings.Contains(err.Error(), providerUUID) || !strings.Contains(err.Error(), "VCPU") {
		t.Errorf("error should name the provider and class: %v", err)
	}
}

// --- provider trait delete ---------------------------------------------------

func TestRunProviderTraitDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/traits", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "latest")
	if err := runProviderTraitDelete(context.Background(), client, providerUUID); err != nil {
		t.Fatalf("runProviderTraitDelete returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
}

func TestRunProviderTraitDelete_PropagatesError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/traits", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := placementClient(fakeServer, "latest")
	err := runProviderTraitDelete(context.Background(), client, providerUUID)
	if err == nil {
		t.Fatal("runProviderTraitDelete = nil, want an error on a 404")
	}
	if !strings.Contains(err.Error(), providerUUID) {
		t.Errorf("error should name the provider: %v", err)
	}
}

// --- provider aggregate set ---------------------------------------------------

// With a symbolic "latest" microversion, runProviderAggregateSet must resolve
// a concrete one (via the version document) before UpdateAggregates parses
// client.Microversion to pick the request-body shape; from 1.19 that shape is
// enveloped with resource_provider_generation.
func TestRunProviderAggregateSet_ResolvesLatestAndEnvelopesTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	agg := "42d4d1d7-1234-4b9e-8f4a-000000000001"
	var putBody map[string]any
	var methods []string
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"versions": [{"max_version": "1.39"}]}`))
	})
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/aggregates", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"resource_provider_generation": 6, "aggregates": ["` + agg + `"]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resource_provider_generation": 5, "aggregates": []}`))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runProviderAggregateSet(context.Background(), client, o, providerUUID, []string{agg}, &buf); err != nil {
		t.Fatalf("runProviderAggregateSet returned error: %v", err)
	}
	// The generation must be read before the write, and the read value carried
	// into it (placement rejects a stale one with 409).
	th.AssertDeepEquals(t, []string{"GET", "PUT"}, methods)
	th.AssertEquals(t, float64(5), putBody["resource_provider_generation"])
	th.AssertDeepEquals(t, []any{agg}, putBody["aggregates"])
	if !strings.Contains(buf.String(), agg) {
		t.Errorf("output missing the aggregate uuid:\n%s", buf.String())
	}
}

// Pinned below 1.19, UpdateAggregates sends the bare array rather than the
// enveloped object — concreteClient must leave an already-numeric
// microversion alone rather than "helpfully" resolving it to latest.
func TestRunProviderAggregateSet_PinnedBelow119SendsTheBareArray(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	agg := "42d4d1d7-1234-4b9e-8f4a-000000000002"
	var rawBody []byte
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a numeric microversion must not trigger a version-document lookup")
		w.WriteHeader(http.StatusOK)
	})
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/aggregates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var err error
			rawBody, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("reading request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"aggregates": ["` + agg + `"]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"aggregates": []}`))
	})

	client := placementClient(fakeServer, "1.14")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runProviderAggregateSet(context.Background(), client, o, providerUUID, []string{agg}, &buf); err != nil {
		t.Fatalf("runProviderAggregateSet returned error: %v", err)
	}
	got := strings.TrimSpace(string(rawBody))
	if !strings.HasPrefix(got, "[") {
		t.Errorf("body for a pre-1.19 microversion must be the bare array, got %q", got)
	}
}

func TestRunProviderAggregateSet_PropagatesReadFailure(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var putAttempted bool
	fakeServer.Mux.HandleFunc("/resource_providers/"+providerUUID+"/aggregates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putAttempted = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := placementClient(fakeServer, "1.20")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runProviderAggregateSet(context.Background(), client, o, providerUUID, []string{"agg"}, &buf)
	if err == nil {
		t.Fatal("runProviderAggregateSet = nil, want an error when reading the current aggregates fails")
	}
	if putAttempted {
		t.Error("the write must not be attempted when the generation read failed")
	}
}

// --- resource usage show -------------------------------------------------------

func TestRunResourceUsageShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const projectID = "11111111-1111-1111-1111-111111111111"
	const userID = "22222222-2222-2222-2222-222222222222"
	var gotQuery string
	fakeServer.Mux.HandleFunc("/usages", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usages": {"INSTANCE": {"VCPU": 5, "consumer_count": 1}}}`))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runResourceUsageShow(context.Background(), client, o, projectID, userID, &buf); err != nil {
		t.Fatalf("runResourceUsageShow returned error: %v", err)
	}
	if !strings.Contains(gotQuery, "project_id="+projectID) {
		t.Errorf("query %q missing project_id", gotQuery)
	}
	if !strings.Contains(gotQuery, "user_id="+userID) {
		t.Errorf("query %q missing user_id", gotQuery)
	}
	out := buf.String()
	for _, want := range []string{"INSTANCE", "VCPU"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunResourceUsageShow_OmitsUserIDWhenNotGiven(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/usages", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usages": {}}`))
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runResourceUsageShow(context.Background(), client, o, "proj-1", "", &buf); err != nil {
		t.Fatalf("runResourceUsageShow returned error: %v", err)
	}
	if strings.Contains(gotQuery, "user_id") {
		t.Errorf("query %q should omit user_id when it was not given", gotQuery)
	}
}

func TestRunResourceUsageShow_PropagatesError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/usages", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := placementClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runResourceUsageShow(context.Background(), client, o, "proj-1", "", &buf)
	if err == nil {
		t.Fatal("runResourceUsageShow = nil, want an error on a 500")
	}
	if !strings.Contains(err.Error(), "proj-1") {
		t.Errorf("error should name the project: %v", err)
	}
}
